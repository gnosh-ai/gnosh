package llm

import (
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// This file is what happens to images for a server that cannot take them:
// a model run without its vision part (a llama.cpp server started without
// --mmproj) or a model that reads text alone. Such a server refuses the
// whole request, and since the conversation keeps a picture a tool read,
// it would refuse every request after it too. So the request is sent again
// with each image as a line of text that says it was left out, and the
// provider sends them that way from then on (provider.noImages).
//
// A server that takes images can still fail to read one: llama.cpp's
// image loader reads no WebP, and a broken file is no picture at all. It
// refuses the whole request too, without saying which image it could not
// read, and the conversation keeps that one as well. The images it has
// taken before are not it, so the others of the request are left out of it
// (provider.unreadable), and the request is sent again; a server that has
// taken every one of them before has them all left out. An image larger
// than the server takes (Anthropic's API: 8000 pixels a side) goes the
// same way, as one too large.
//
// Anthropic's API also holds each image of a request with more than 20 to
// a lower limit, which a picture it took before can be over: a session
// that read many screenshots was refused from its 21st on. Once it says
// so, only the newest images of a request are sent whole
// (provider.fewImages).

// imageLeftOut stands in a request where an image was.
const imageLeftOut = "[An image was left out here: the model in use cannot take images.]"

// imageUnread stands in a request where an image was that the server
// could not read.
const imageUnread = "[An image was left out here: the model's server could not read it (a format it does not read, such as WebP, or a broken file).]"

// imageTooLarge stands in a request where an image was that is larger
// than the model's server takes.
const imageTooLarge = "[An image was left out here: it is larger than the model's server takes. Crop or shrink it, and read the smaller copy.]"

// imageOlder stands in a request where an image was, one of the older of
// a request that holds more than its server takes whole.
const imageOlder = "[An image was left out here: the model's server takes only so many images in a request, and this is one of the older ones.]"

// tooLarge are what servers say, in lower case, when an image of the
// request is larger than they take: Anthropic's API for one over its size
// or pixel limits.
var tooLarge = []string{
	"image exceeds",
	"image dimensions exceed",
	"image is too large",
	"image too large",
}

// manyImages is what Anthropic's API says, in lower case, when it holds
// the images of a request with many of them to its lower limit.
var manyImages = []string{"many-image"}

// manyImagesKept is how many images a request to a server that refused
// many holds whole: the newest, as many as Anthropic's API takes before
// its lower limit applies.
const manyImagesKept = 20

// unreadable are what servers that take images say, in lower case, when
// they could not read one: llama.cpp's server ("Failed to load image or
// audio file") and servers that read pictures with Python's PIL ("cannot
// identify image file").
var unreadable = []string{
	"failed to load image",
	"cannot identify image file",
	"could not process image",
}

// refusals are what servers say when a request holds an image they cannot
// take, in lower case: llama.cpp's server ("image input is not supported -
// hint: if this is unexpected, you may need to provide the mmproj"), and
// the words of servers that serve a model for text alone.
var refusals = []string{
	"image input is not supported",
	"images are not supported",
	"image inputs are not supported",
	"does not support image",
	"not a multimodal model",
	"multimodal input is not supported",
	"vision is not supported",
}

// LeavesOutImages reports whether the server has refused the images of a
// request for model, so that each is now sent to it as a line saying it
// was left out (core.ImageDropper).
func (p *provider) LeavesOutImages(model string) bool {
	_, ok := p.noImages.Load(model)
	return ok
}

// imagesRefused reports whether err is a server saying it cannot take
// the request's images.
func imagesRefused(err error) bool { return serverSays(err, refusals) }

// imageUnreadable reports whether err is a server saying it could not read
// an image of the request.
func imageUnreadable(err error) bool { return serverSays(err, unreadable) }

// serverSays reports whether err is a server's refusal (a status of 400 or
// more) whose words hold one of these, in lower case.
func serverSays(err error, these []string) bool {
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Status < 400 {
		return false
	}
	said := strings.ToLower(pe.Message + "\n" + pe.Body)
	for _, r := range these {
		if strings.Contains(said, r) {
			return true
		}
	}
	return false
}

// imageKey is what an image is known by: its type and its bytes, hashed.
type imageKey [sha256.Size]byte

func keyOf(b core.Block) imageKey {
	h := sha256.New()
	h.Write([]byte(b.MediaType))
	h.Write([]byte{0})
	h.Write(b.Data)
	var k imageKey
	h.Sum(k[:0])
	return k
}

// eachImage calls f with every image of req, in messages and in tools'
// results.
func eachImage(req *core.Request, f func(core.Block)) {
	var walk func([]core.Block)
	walk = func(bs []core.Block) {
		for _, b := range bs {
			switch b.Type {
			case core.BlockImage:
				f(b)
			case core.BlockToolResult:
				walk(b.Content)
			}
		}
	}
	for _, m := range req.Messages {
		walk(m.Blocks)
	}
}

// prepared is req as the server is sent it: without images at all when it
// has refused them for this model, and otherwise without those it could
// not read or would not take, and with only the newest whole when it has
// refused a request for holding many.
func (p *provider) prepared(req *core.Request) *core.Request {
	if p.LeavesOutImages(req.Model) {
		return withoutImages(req)
	}
	older := 0
	if _, few := p.fewImages.Load(req.Model); few {
		older = max(countImages(req)-manyImagesKept, 0)
	}
	seen := 0
	return replacing(req, func(b core.Block) (string, bool) {
		seen++
		if line, bad := p.unreadable.Load(keyOf(b)); bad {
			return line.(string), true
		}
		if seen <= older {
			return imageOlder, true
		}
		return "", false
	})
}

// countImages is how many images req holds, in messages and in tools'
// results.
func countImages(req *core.Request) int {
	n := 0
	eachImage(req, func(core.Block) { n++ })
	return n
}

// took records the images of a request the server answered: it could read
// them.
func (p *provider) took(send *core.Request) {
	eachImage(send, func(b core.Block) { p.taken.Store(keyOf(b), struct{}{}) })
}

// afterImageError says what to send in place of send, the form of req that
// failed with err, when err is a server that took none of its images or
// could not read one; again is false for any other failure, which is the
// caller's. Each answer leaves out at least one image more than send held,
// so the tries come to an end.
func (p *provider) afterImageError(req, send *core.Request, err error) (next *core.Request, again bool) {
	if !hasImages(send) {
		return nil, false
	}
	switch {
	case imagesRefused(err):
		// The server takes no images: from now on it is sent none.
		p.noImages.Store(req.Model, true)
		return withoutImages(req), true
	case serverSays(err, manyImages):
		// From now on only the newest are sent whole. A request that held
		// no more than those was refused for something else.
		if countImages(send) <= manyImagesKept {
			return nil, false
		}
		p.fewImages.Store(req.Model, true)
		return p.prepared(req), true
	case imageUnreadable(err), serverSays(err, tooLarge):
		// One it could not read is among those it has not taken before;
		// with none such, it is any of them.
		line := imageUnread
		if !imageUnreadable(err) {
			line = imageTooLarge
		}
		var fresh, all []imageKey
		eachImage(send, func(b core.Block) {
			k := keyOf(b)
			all = append(all, k)
			if _, ok := p.taken.Load(k); !ok {
				fresh = append(fresh, k)
			}
		})
		if len(fresh) == 0 {
			fresh = all
		}
		for _, k := range fresh {
			p.unreadable.Store(k, line)
		}
		return p.prepared(req), true
	}
	return nil, false
}

// hasImages reports whether a request holds an image, in a message or in
// a tool's result.
func hasImages(req *core.Request) bool {
	for _, m := range req.Messages {
		if blocksHaveImage(m.Blocks) {
			return true
		}
	}
	return false
}

func blocksHaveImage(bs []core.Block) bool {
	for _, b := range bs {
		if b.Type == core.BlockImage || b.Type == core.BlockToolResult && blocksHaveImage(b.Content) {
			return true
		}
	}
	return false
}

// withoutImages returns a copy of req with each image a line of text
// that says it was left out. req itself, the conversation's, is left as it
// was, so a model that can see is shown the pictures again.
func withoutImages(req *core.Request) *core.Request {
	return replacing(req, func(core.Block) (string, bool) { return imageLeftOut, true })
}

// replacing returns req with each image for which with says so as a line
// of text, the one with gives; req itself is left as it was, and returned
// when no image is to go.
func replacing(req *core.Request, with func(core.Block) (string, bool)) *core.Request {
	if !hasImages(req) {
		return req
	}
	var out *core.Request
	for i, m := range req.Messages {
		blocks, changed := replaceImages(m.Blocks, with)
		if !changed {
			continue
		}
		if out == nil {
			c := *req
			c.Messages = append([]core.Message(nil), req.Messages...)
			out = &c
		}
		m.Blocks = blocks
		out.Messages[i] = m
	}
	if out == nil {
		return req
	}
	return out
}

// replaceImages is bs with each image for which with says so as a line of
// text; changed says one was.
func replaceImages(bs []core.Block, with func(core.Block) (string, bool)) (out []core.Block, changed bool) {
	if !blocksHaveImage(bs) {
		return bs, false
	}
	out = make([]core.Block, len(bs))
	for i, b := range bs {
		switch b.Type {
		case core.BlockImage:
			if text, ok := with(b); ok {
				b, changed = core.Text(text), true
			}
		case core.BlockToolResult:
			var c bool
			if b.Content, c = replaceImages(b.Content, with); c {
				changed = true
			}
		}
		out[i] = b
	}
	return out, changed
}
