package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// llamaNoMmproj is what a llama.cpp server started without its vision part
// answers a request with an image in it.
const llamaNoMmproj = `{"error":{"code":500,"message":"image input is not supported - hint: if this is unexpected, you may need to provide the mmproj","type":"server_error"}}`

// refusingImages is a server that refuses any request with an image in it
// as status and body say, and answers the others.
func refusingImages(status int, body string) func(*fake, http.ResponseWriter, *http.Request) {
	return func(f *fake, w http.ResponseWriter, r *http.Request) {
		if f.last().Body != nil && bytes.Contains(f.last().Body, []byte("image_url")) {
			answering(status, nil, body)(f, w, r)
			return
		}
		f.play(w, r, okStream)
	}
}

// readAPicture is a conversation in which a tool read a picture.
func readAPicture() *core.Request {
	return &core.Request{Model: "m", Messages: []core.Message{
		core.UserText("look at the page"),
		{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("call_1", "Read", json.RawMessage(`{"file_path":"shot.png"}`))}},
		{Role: core.RoleUser, Blocks: []core.Block{
			{Type: core.BlockToolResult, ToolUseID: "call_1", Content: []core.Block{
				core.Text("[shot.png: a PNG image, 1 KB.]"),
				{Type: core.BlockImage, MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nfake")},
			}},
		}},
	}}
}

// A server that cannot take images is sent the request again at once
// without them, each a line that says it was left out, and is never sent
// one again; the conversation keeps its pictures.
func TestImagesAServerRefusesAreLeftOut(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		status    int
		body      string
	}{
		{"llama.cpp without its mmproj", TypeLlamaCpp, 500, llamaNoMmproj},
		{"a server whose model reads text alone", TypeOpenAI, 400, `{"error":{"message":"m is not a multimodal model","type":"BadRequestError","code":400}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, refusingImages(c.status, c.body))
			p := f.provider(c.typ, nil)
			req := readAPicture()
			res := drain(p.Stream(context.Background(), req))
			wantOK(t, res)
			reqs := f.requests()
			if len(reqs) != 2 {
				t.Fatalf("%d requests, want the refused one and the one without its image", len(reqs))
			}
			if !bytes.Contains(reqs[0].Body, []byte("image_url")) || bytes.Contains(reqs[1].Body, []byte("image_url")) {
				t.Errorf("the first request should hold the image and the second not:\n%s\n%s", reqs[0].Body, reqs[1].Body)
			}
			if !bytes.Contains(reqs[1].Body, []byte("left out here: the model in use cannot take images")) {
				t.Errorf("the second request does not say the image was left out:\n%s", reqs[1].Body)
			}
			if req.Messages[2].Blocks[0].Content[1].Type != core.BlockImage {
				t.Errorf("the conversation lost its picture")
			}
			// From now on the image is left out from the start.
			wantOK(t, drain(p.Stream(context.Background(), readAPicture())))
			if reqs := f.requests(); len(reqs) != 3 || bytes.Contains(reqs[2].Body, []byte("image_url")) {
				t.Errorf("after the refusal: %d requests, the last with an image: %v", len(reqs), len(reqs) == 3 && bytes.Contains(reqs[2].Body, []byte("image_url")))
			}
		})
	}
}

// Any other failure is the caller's, as before: the request is not sent
// again here, with an image or without one.
func TestOtherFailuresAreNotSentAgain(t *testing.T) {
	f := newFake(t, answering(500, nil, `{"error":{"code":500,"message":"the KV cache is full","type":"server_error"}}`))
	p := f.provider(TypeLlamaCpp, nil)
	res := drain(p.Stream(context.Background(), readAPicture()))
	wantFailure(t, res.err, core.ErrServer)
	if n := len(f.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}

	// A refusal of images for a request that holds none is not one.
	g := newFake(t, answering(500, nil, llamaNoMmproj))
	q := g.provider(TypeLlamaCpp, nil)
	wantFailure(t, ask(q).err, core.ErrServer)
	if n := len(g.requests()); n != 1 {
		t.Errorf("%d requests for a request with no image, want 1", n)
	}
}

// withoutImages leaves the request it is given as it was, and takes out
// images in messages and in tools' results alike.
func TestWithoutImages(t *testing.T) {
	req := readAPicture()
	req.Messages = append(req.Messages, core.Message{Role: core.RoleUser, Blocks: []core.Block{
		core.Text("and this one"), {Type: core.BlockImage, MediaType: "image/jpeg", Data: []byte("jpeg")},
	}})
	out := withoutImages(req)
	if hasImages(out) || !hasImages(req) {
		t.Fatalf("hasImages: copy %v, original %v; want false, true", hasImages(out), hasImages(req))
	}
	got := core.TextOf(out.Messages[2].Blocks[0].Content) + "|" + core.TextOf(out.Messages[3].Blocks)
	if strings.Count(got, imageLeftOut) != 2 || !strings.Contains(got, "and this one") || !strings.Contains(got, "a PNG image") {
		t.Errorf("the copy reads %q", got)
	}
	plain := &core.Request{Messages: []core.Message{core.UserText("hi")}}
	if withoutImages(plain) != plain {
		t.Errorf("a request with no image was copied")
	}
}

// llamaUnreadable is what a llama.cpp server with its vision part answers
// a request holding an image it could not read (a WebP, a broken file).
const llamaUnreadable = `{"error":{"code":400,"message":"Failed to load image or audio file","type":"invalid_request_error"}}`

// A server that takes images but could not read one is sent the request
// again without the images it had not taken before, each a line that says
// it could not read it; the pictures it took before still go, and the one
// it could not read is left out from then on.
func TestAnImageTheServerCouldNotReadIsLeftOut(t *testing.T) {
	bad := []byte("RIFF\x1a\x00\x00\x00WEBPVP8L")
	badText := []byte(base64.StdEncoding.EncodeToString(bad))
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if bytes.Contains(f.last().Body, badText) {
			answering(400, nil, llamaUnreadable)(f, w, r)
			return
		}
		f.play(w, r, okStream)
	})
	p := f.provider(TypeLlamaCpp, nil)
	wantOK(t, drain(p.Stream(context.Background(), readAPicture())))

	withBoth := func() *core.Request {
		req := readAPicture()
		req.Messages = append(req.Messages, core.Message{Role: core.RoleUser, Blocks: []core.Block{
			core.Text("and this one"), {Type: core.BlockImage, MediaType: "image/webp", Data: bad},
		}})
		return req
	}
	req := withBoth()
	wantOK(t, drain(p.Stream(context.Background(), req)))
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the first picture's, the refused one and the one without the WebP", len(reqs))
	}
	last := reqs[2].Body
	if bytes.Contains(last, badText) || bytes.Count(last, []byte(`"type":"image_url"`)) != 1 {
		t.Errorf("the request sent again should hold the PNG and not the WebP:\n%s", last)
	}
	if !bytes.Contains(last, []byte("could not read it")) || bytes.Contains(last, []byte("cannot take images")) {
		t.Errorf("the request sent again does not say the server could not read the WebP:\n%s", last)
	}
	if req.Messages[3].Blocks[1].Type != core.BlockImage {
		t.Errorf("the conversation lost its picture")
	}
	if p.(core.ImageDropper).LeavesOutImages("m") {
		t.Errorf("a server that could not read one image is not one that takes none")
	}
	// From then on the WebP is left out from the start, and the PNG goes.
	wantOK(t, drain(p.Stream(context.Background(), withBoth())))
	if reqs := f.requests(); len(reqs) != 4 || bytes.Contains(reqs[3].Body, badText) || !bytes.Contains(reqs[3].Body, []byte("image_url")) {
		t.Errorf("after it: %d requests, the last with the WebP %v", len(reqs), len(reqs) == 4 && bytes.Contains(reqs[3].Body, badText))
	}
}

// A server that says it could not read an image of a request whose images
// it has all taken before has them all left out, and the tries end.
func TestUnreadableImagesItTookBeforeAreAllLeftOut(t *testing.T) {
	answered := 0
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if answered > 0 && bytes.Contains(f.last().Body, []byte("image_url")) {
			answering(400, nil, llamaUnreadable)(f, w, r)
			return
		}
		answered++
		f.play(w, r, okStream)
	})
	p := f.provider(TypeLlamaCpp, nil)
	wantOK(t, drain(p.Stream(context.Background(), readAPicture())))
	wantOK(t, drain(p.Stream(context.Background(), readAPicture())))
	reqs := f.requests()
	if len(reqs) != 3 || bytes.Contains(reqs[2].Body, []byte("image_url")) {
		t.Errorf("%d requests, the last with an image: want 3, the last with none", len(reqs))
	}

	// One that fails every request with an image ends its tries too.
	g := newFake(t, refusingImages(400, llamaUnreadable))
	q := g.provider(TypeLlamaCpp, nil)
	wantOK(t, drain(q.Stream(context.Background(), readAPicture())))
	if n := len(g.requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

// A server that refused the images of one model may have the eyes for
// another it serves: the next model is sent its images.
func TestImagesAreLeftOutForTheModelThatRefusedThem(t *testing.T) {
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		body := f.last().Body
		if bytes.Contains(body, []byte("image_url")) && bytes.Contains(body, []byte(`"model":"m"`)) {
			answering(500, nil, llamaNoMmproj)(f, w, r)
			return
		}
		f.play(w, r, okStream)
	})
	p := f.provider(TypeLlamaCpp, nil)
	wantOK(t, drain(p.Stream(context.Background(), readAPicture())))
	other := readAPicture()
	other.Model = "vision"
	wantOK(t, drain(p.Stream(context.Background(), other)))
	reqs := f.requests()
	if len(reqs) != 3 || !bytes.Contains(reqs[2].Body, []byte("image_url")) {
		t.Errorf("the other model was not sent its image (%d requests)", len(reqs))
	}
	if d := p.(core.ImageDropper); !d.LeavesOutImages("m") || d.LeavesOutImages("vision") {
		t.Errorf("LeavesOutImages: m %v, vision %v; want true, false", d.LeavesOutImages("m"), d.LeavesOutImages("vision"))
	}
}

// manyPictures is a conversation in which a tool read n pictures, each
// different.
func manyPictures(n int) *core.Request {
	req := &core.Request{Model: "claude", Messages: []core.Message{core.UserText("look at the pages")}}
	for i := range n {
		id := "call_" + strconv.Itoa(i)
		req.Messages = append(req.Messages,
			core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse(id, "Read", json.RawMessage(`{"file_path":"shot.png"}`))}},
			core.Message{Role: core.RoleUser, Blocks: []core.Block{{Type: core.BlockToolResult, ToolUseID: id, Content: []core.Block{
				{Type: core.BlockImage, MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nshot " + strconv.Itoa(i))},
			}}}})
	}
	return req
}

// An image larger than Anthropic's API takes is left out of the request,
// as one too large, and the request is sent again; the conversation keeps
// it. It refused the request, and the conversation kept it, so every
// later request was refused too (the model client review of 2026-11-19).
func TestAnImageTooLargeIsLeftOut(t *testing.T) {
	big := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nshot 1"))
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if bytes.Contains(f.last().Body, []byte(big)) {
			answering(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.4.content.0.content.0.image.source.base64.data: At least one of the image dimensions exceed max allowed size: 8000 pixels"}}`)(f, w, r)
			return
		}
		f.play(w, r, events(messageDelta("end_turn", ""), messageStop))
	})
	p := f.provider(TypeAnthropic, func(c *Config) { c.PromptCache = "off" })
	wantOK(t, drain(p.Stream(context.Background(), manyPictures(1))))
	req := manyPictures(2)
	wantOK(t, drain(p.Stream(context.Background(), req)))
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the first picture's, the refused one and the one without the large one", len(reqs))
	}
	last := reqs[2].Body
	if bytes.Contains(last, []byte(big)) || bytes.Count(last, []byte(`"type":"image"`)) != 1 || !bytes.Contains(last, []byte("larger than the model")) {
		t.Errorf("the request sent again should hold the first picture and say the second was too large:\n%s", last)
	}
	if req.Messages[4].Blocks[0].Content[0].Type != core.BlockImage {
		t.Errorf("the conversation lost its picture")
	}
}

// When Anthropic's API refuses a request for holding many images (more
// than 20 are held to a lower limit, which a picture it took before can be
// over), only the newest 20 are sent whole, then and from then on; the
// older are lines that say so. A session that read many screenshots was
// refused from its 21st on (the model client review of 2026-11-19).
func TestManyImagesKeepTheNewest(t *testing.T) {
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if bytes.Count(f.last().Body, []byte(`"type":"image"`)) > 20 {
			answering(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.2.content.0.content.0.image.source.base64.data: Image dimensions exceed the 2000 pixel limit for many-image requests"}}`)(f, w, r)
			return
		}
		f.play(w, r, events(messageDelta("end_turn", ""), messageStop))
	})
	p := f.provider(TypeAnthropic, func(c *Config) { c.PromptCache = "off" })
	wantOK(t, drain(p.Stream(context.Background(), manyPictures(22))))
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want the refused one and the one with the newest 20", len(reqs))
	}
	oldest := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nshot 0"))
	newest := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nshot 21"))
	last := reqs[1].Body
	if n := bytes.Count(last, []byte(`"type":"image"`)); n != 20 {
		t.Errorf("the request sent again holds %d images", n)
	}
	if bytes.Contains(last, []byte(oldest)) || !bytes.Contains(last, []byte(newest)) {
		t.Errorf("the request sent again holds the oldest (%v) or lacks the newest (%v)", bytes.Contains(last, []byte(oldest)), !bytes.Contains(last, []byte(newest)))
	}
	if n := bytes.Count(last, []byte("one of the older ones")); n != 2 {
		t.Errorf("%d older images are said to be left out, want 2", n)
	}
	// From then on the newest 20 go from the start.
	wantOK(t, drain(p.Stream(context.Background(), manyPictures(23))))
	if reqs := f.requests(); len(reqs) != 3 || bytes.Count(reqs[2].Body, []byte(`"type":"image"`)) != 20 {
		t.Errorf("after it: %d requests", len(reqs))
	}
	// A request with no more than 20 that is refused so is not this.
	f2 := newFake(t, answering(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"many-image requests are held to a lower limit"}}`))
	p2 := f2.provider(TypeAnthropic, func(c *Config) { c.PromptCache = "off" })
	wantFailure(t, drain(p2.Stream(context.Background(), manyPictures(3))).err, core.ErrInvalidRequest)
	if n := len(f2.requests()); n != 1 {
		t.Errorf("a refusal with 3 images was sent %d times", n)
	}
}
