package config

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// ProviderExample is the least a config file needs for a turn to run: one
// model server on this machine. It is what the messages that say "add a
// provider" show, so that they all show the same thing.
const ProviderExample = `"providers": {"local": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}`

// readFile reads the text of a config file into ops. Every mistake is
// recorded with its line, and a setting with a mistake gives no op.
//
// The ops come out in one fixed order, whatever order the file has its
// keys in: providers, then the models registered on them, then the two
// model choices, then hosts, options, permissions, hooks, the env block
// and disableAllHooks. So a model may name a provider that the same file
// declares further down, and what an untrusted folder's file holds is
// listed the same way however the file is laid out.
func readFile(s *source, data []byte) {
	start := len(s.probs.list)
	defer func() {
		// The mistakes of one file, in the order of its lines.
		slices.SortStableFunc(s.probs.list[start:], func(a, b Problem) int { return a.Line - b.Line })
	}()

	root, jerr := parseJSON(data)
	if jerr != nil {
		s.errorf(jerr.line, "this file is not JSON gnosh can read (%s), so none of it is in force. A config file is JSON in which // and /* */ comments and a comma after the last item are allowed, and nothing else is.", clip(jerr.msg, 160))
		return
	}
	if root.kind != jObject {
		s.errorf(root.line, "a config file holds one JSON object, in braces, with the keys %s. This one holds %s, so none of it is in force.", keyNames(topKeys), root.kind.word())
		return
	}
	given := map[string]jfield{}
	for _, f := range s.unique(root, "") {
		if !hasKey(topKeys, f.key) {
			s.unknownKey(f)
			continue
		}
		given[f.key] = f
	}
	for _, key := range []string{"providers", "models", "model", "smallModel", "fallbackModel", "hosts", "options", "permissions", "hooks", "env", "disableAllHooks", "statusLine", "fileSuggestion", "attribution", "includeCoAuthoredBy", "agent", "skillOverrides"} {
		f, ok := given[key]
		if !ok {
			continue
		}
		switch key {
		case "providers":
			s.providers(f)
		case "models":
			s.models(f)
		case "model", "smallModel":
			s.slot(f)
		case "fallbackModel":
			s.fallbackModel(f)
		case "hosts":
			s.hosts(f)
		case "options":
			s.options(f)
		case "permissions":
			s.settingsPermissions(f)
		case "hooks":
			s.settingsHooks(f)
		case "env":
			s.settingsEnv(f)
		case "skillOverrides":
			s.settingsSkillOverrides(f)
		case "statusLine":
			s.statusLine(f)
		case "fileSuggestion":
			s.fileSuggestion(f)
		case "attribution":
			s.attribution(f)
		case "includeCoAuthoredBy":
			// Claude Code's older word for leaving its own attribution out.
			// gnosh adds none of its own, so there is nothing for it to
			// leave out: it is taken, and changes nothing.
			if f.val.kind != jBool {
				s.errorf(f.line, "\"includeCoAuthoredBy\" is true or false, and here it is %s.", f.val.kind.word())
			}
		case "agent":
			if f.val.kind != jString || strings.TrimSpace(f.val.str) == "" {
				s.errorf(f.line, "\"agent\" is the name of a kind of agent for the session to run as, as a string, and here it is %s.", f.val.kind.word())
				continue
			}
			name, o := strings.TrimSpace(f.val.str), s.at(f.line)
			s.add(op{kind: KindOption, text: "agent: " + name, origin: o, apply: func(b *builder) { b.agent.put(name, o) }})
		case "disableAllHooks":
			if f.val.kind != jBool {
				s.errorf(f.line, "\"disableAllHooks\" is true or false, and here it is %s.", f.val.kind.word())
				continue
			}
			off, o := f.val.b, s.at(f.line)
			s.add(op{kind: KindHookOff, text: "disableAllHooks: " + strconv.FormatBool(off), origin: o,
				apply: func(b *builder) { b.hooksOff.put(off, o) }})
		}
	}
}

// unknownKey says what a key at the top of a config file is, when it is
// not one gnosh reads there.
func (s *source) unknownKey(f jfield) {
	switch {
	case findOption(f.key) != nil:
		s.errorf(f.line, "%q is one of gnosh's options, and those go inside \"options\": write \"options\": {%q: …}.", f.key, f.key)
	case slices.Contains(claudeCodeKeys, f.key):
		s.errorf(f.line, "%q is a setting Claude Code has and gnosh has not built, so it would do nothing here. Take it out of this file.", f.key)
	default:
		s.errorf(f.line, "%s is not a key gnosh knows. The keys of a config file are: %s.", showName(f.key), keyNames(topKeys))
	}
}

// failed reports whether a mistake was recorded since the mark, which is
// the number of problems there were then.
func (s *source) failed(mark int) bool { return len(s.probs.list) != mark }

// shown is a value for a message that says a value has the wrong shape: a
// number as it is written, and for anything else only its kind, since
// what stands there may be a key that landed in the wrong place.
func shown(n *jnode) string {
	if n.kind == jNumber {
		return clip(n.str, 40)
	}
	return n.kind.word()
}

// plain turns a JSON value into the plain Go value the standard library
// would give for it, with numbers kept as they are written. A key given
// twice in an object is a mistake here as everywhere.
func (s *source) plain(n *jnode, under string) any {
	switch n.kind {
	case jObject:
		m := make(map[string]any, len(n.fields))
		for _, f := range s.unique(n, under) {
			m[f.key] = s.plain(f.val, under+f.key+".")
		}
		return m
	case jArray:
		out := make([]any, len(n.items))
		for i, it := range n.items {
			out[i] = s.plain(it, under)
		}
		return out
	case jString:
		return n.str
	case jNumber:
		return json.Number(n.str)
	case jBool:
		return n.b
	}
	return nil
}

// unsetVars are the variables one field of a provider is filled in from
// that are not set and have no default.
type unsetVars struct {
	names  []string
	origin Origin
}

// providerChange is what one file says of a provider. A nil field was not
// given and is left as it was.
type providerChange struct {
	typ          *providerType
	name         *string
	urls         []string
	hasURLs      bool
	key          *Secret
	keyFrom      string
	headers      []Header
	hasHeaders   bool
	extraBody    map[string]any
	hasExtraBody bool
	listModels   *bool
	noListModels bool // "listModels": null
	disabled     *bool
	// at is where each field stands that sets a value with a place of its
	// own.
	listModelsAt Origin
	// filled are the fields given here that take variables, and unset
	// those of them whose variables are not all set.
	filled []string
	unset  map[string]unsetVars
}

// providers reads the "providers" object.
func (s *source) providers(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"providers\" is an object with one member for each model server, under an id of your choosing, as in %s. Here it is %s, so none of it is in force.", ProviderExample, f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "providers.") {
		id, o := m.key, s.at(m.line)
		switch {
		case !idPattern.MatchString(id):
			s.errorf(m.line, "%s is not a usable id for a provider: use letters, digits, dots, dashes and underscores, starting with a letter or a digit.", showName(id))
		case m.val.kind == jNull:
			s.add(op{kind: KindRemoval, text: "providers." + id + ": null (takes the provider away)", origin: o,
				apply: func(b *builder) { b.removeProvider(id, o) }})
		case m.val.kind != jObject:
			s.errorf(m.line, "the provider %s is an object with the fields %s, or null to take away a provider that a weaker file declared. Here it is %s.", id, keyNames(providerFields), m.val.kind.word())
		default:
			s.provider(id, m)
		}
	}
}

// fillIn fills in the variables of one string value of a provider. ok is
// false when the value is written wrongly, which has been recorded.
func (s *source) fillIn(line int, key, text string) (filled, bool) {
	got, why := fill(text, s.getenv)
	if why != "" {
		s.errorf(line, "%q %s.", key, why)
		return got, false
	}
	return got, true
}

// provider reads one provider: its fields, with the variables of its
// addresses, its key and its header values filled in.
func (s *source) provider(id string, m jfield) {
	mark := len(s.probs.list)
	ch := providerChange{unset: map[string]unsetVars{}}
	var addresses, reads []string
	// took notes what filling in a field's value gave: the variables it
	// names, and those of them that are not set.
	took := func(field string, line int, got filled) {
		for _, name := range got.vars {
			if !slices.Contains(reads, name) {
				reads = append(reads, name)
			}
		}
		if len(got.unset) == 0 {
			return
		}
		u := ch.unset[field]
		for _, name := range got.unset {
			if !slices.Contains(u.names, name) {
				u.names = append(u.names, name)
			}
		}
		if u.origin.Line == 0 {
			u.origin = s.at(line)
		}
		ch.unset[field] = u
	}
	for _, fl := range s.unique(m.val, "providers."+id+".") {
		key := "providers." + id + "." + fl.key
		v := fl.val
		switch fl.key {
		case "type":
			switch {
			case v.kind == jNull:
				s.errorf(fl.line, "%q cannot be null: a provider keeps its type. To take the whole provider away, write null for the provider itself.", key)
			case v.kind != jString:
				s.errorf(fl.line, "%q is one of %s, as a string, and here it is %s.", key, list(providerTypeNames()), shown(v))
			default:
				if ch.typ = findProviderType(v.str); ch.typ == nil {
					s.errorf(fl.line, "%s is not a provider type gnosh knows. The types are: %s.", showName(v.str), list(providerTypeNames()))
				}
			}
		case "name":
			switch v.kind {
			case jNull:
				ch.name = new(string)
			case jString:
				ch.name = &v.str
			default:
				s.errorf(fl.line, "%q is the name to show, as a string, and here it is %s.", key, shown(v))
			}
		case "baseUrl":
			ch.hasURLs = true
			ch.filled = append(ch.filled, "baseUrl")
			var given []*jnode
			switch items, isList := v.strings(); {
			case v.kind == jNull:
			case v.kind == jString:
				given = []*jnode{v}
			case isList && len(items) > 0:
				given = items
			default:
				s.errorf(fl.line, "%q is an address as a string, such as \"http://127.0.0.1:8080\", or a list of addresses to try in order (null takes away what a weaker file gave). Here it is %s.", key, emptyOr(v))
			}
			for _, a := range given {
				got, ok := s.fillIn(a.line, key, a.str)
				if !ok {
					continue
				}
				took("baseUrl", a.line, got)
				if len(got.unset) > 0 {
					addresses = append(addresses, "an address that is not known yet")
					continue
				}
				s.secrets.addURL(got.text)
				if why := checkURL(got.text); why != "" {
					s.errorf(a.line, "the address given in %q cannot be used: %s. An address looks like http://127.0.0.1:8080.", key, why)
					continue
				}
				ch.urls = append(ch.urls, got.text)
				if len(got.vars) > 0 {
					// The address is made with variables of the person's.
					// What it comes to is not put into words that a
					// stranger's file could have chosen.
					addresses = append(addresses, "an address filled in from the environment")
				} else {
					addresses = append(addresses, DisplayURL(got.text))
				}
			}
		case "apiKey":
			ch.filled = append(ch.filled, "apiKey")
			switch v.kind {
			case jNull:
				ch.key = new(Secret)
			case jString:
				got, ok := s.fillIn(fl.line, key, v.str)
				if !ok {
					// What is written wrongly may still be a key.
					s.secrets.add(v.str)
					break
				}
				took("apiKey", fl.line, got)
				s.secrets.add(got.text)
				if len(got.unset) > 0 {
					// A key with a piece missing is no key: the provider
					// waits for the variable, and nothing half filled in
					// is kept to be sent.
					ch.key = new(Secret)
					break
				}
				secret := NewSecret(got.text)
				ch.key = &secret
				if got.only != "" {
					ch.keyFrom = "$" + got.only
				}
			default:
				s.errorf(fl.line, "%q is the key, as a string, and here it is %s. (What stands there is not shown.)", key, v.kind.word())
			}
		case "headers":
			ch.hasHeaders = true
			ch.filled = append(ch.filled, "headers")
			switch v.kind {
			case jNull:
			case jObject:
				for _, h := range s.unique(v, key+".") {
					switch {
					case !isHeaderName(h.key):
						s.errorf(h.line, "%s is not a usable HTTP header name under %q.", showName(h.key), key)
					case h.val.kind != jString:
						s.errorf(h.line, "the value of the header %s under %q is a string, and here it is %s. (What stands there is not shown.)", h.key, key, h.val.kind.word())
					default:
						got, ok := s.fillIn(h.line, key+"."+h.key, h.val.str)
						if !ok {
							s.secrets.add(h.val.str)
							continue
						}
						took("headers", h.line, got)
						s.secrets.add(got.text)
						if len(got.unset) == 0 {
							ch.headers = append(ch.headers, Header{Name: h.key, Value: NewSecret(got.text)})
						}
					}
				}
			default:
				s.errorf(fl.line, "%q is an object of header names and their values, such as {\"X-Title\": \"gnosh\"}, and here it is %s.", key, v.kind.word())
			}
		case "extraBody":
			ch.hasExtraBody = true
			switch v.kind {
			case jNull:
			case jObject:
				ch.extraBody, _ = s.plain(v, key+".").(map[string]any)
			default:
				s.errorf(fl.line, "%q is a JSON object that is merged into the body of every request, such as {\"id_slot\": 1}, and here it is %s.", key, shown(v))
			}
		case "listModels":
			switch v.kind {
			case jNull:
				ch.noListModels = true
			case jBool:
				ch.listModels, ch.listModelsAt = &v.b, s.at(fl.line)
			default:
				s.errorf(fl.line, "%q is true or false, and here it is %s.", key, shown(v))
			}
		case "disabled":
			switch v.kind {
			case jNull:
				ch.disabled = new(bool)
			case jBool:
				ch.disabled = &v.b
			default:
				s.errorf(fl.line, "%q is true or false, and here it is %s.", key, shown(v))
			}
		default:
			s.errorf(fl.line, "%s is not a field of a provider. The fields are: %s.", showName(fl.key), keyNames(providerFields))
		}
	}
	if s.failed(mark) {
		return
	}
	text := "provider " + id
	if ch.typ != nil {
		text += " (" + ch.typ.name + ")"
	}
	for _, a := range addresses {
		text += " at " + a
	}
	if len(reads) > 0 {
		text += ", reading $" + strings.Join(reads, ", $")
	}
	o := s.at(m.line)
	s.add(op{kind: KindProvider, text: text, origin: o, apply: func(b *builder) { b.addProvider(id, ch, o) }})
}

// emptyOr is shown for a list that was expected to hold something: "an
// empty list", or what the value is.
func emptyOr(n *jnode) string {
	if n.kind == jArray && len(n.items) == 0 {
		return "an empty list"
	}
	if n.kind == jArray {
		return "a list that holds more than strings"
	}
	return shown(n)
}

// modelRef splits "<provider>/<model id>" at its first slash. where names
// the key the name stands under, for a message.
func (s *source) modelRef(line int, text, where string) (ModelRef, bool) {
	prov, id, ok := strings.Cut(text, "/")
	if !ok || prov == "" || id == "" {
		s.errorf(line, "%s names a model as \"<provider>/<model id>\", the form gnosh models prints; %s has no provider in front.", where, showName(text))
		return ModelRef{}, false
	}
	if !idPattern.MatchString(prov) || strings.ContainsAny(id, " ") || !printable(id) || len(id) > 256 {
		s.errorf(line, "%s is not a usable model name under %s: it is written \"<provider>/<model id>\".", showName(text), where)
		return ModelRef{}, false
	}
	return ModelRef{Provider: prov, ID: id}, true
}

// modelChange is what one file says of a registered model. A field that
// was not given is left as it was; one given as null is put back to not
// set.
type modelChange struct {
	name       *string
	window     *int
	noWindow   bool
	maxOutput  *int
	noMax      bool
	levels     []string
	hasLevels  bool
	noLevels   bool
	budget     *int
	noBudget   bool
	windowAt   Origin
	maxAt      Origin
	levelsAt   Origin
	budgetAt   Origin
	registered Origin
}

// models reads the "models" object.
func (s *source) models(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"models\" is an object with one member for each model gnosh should know something about, under \"<provider>/<model id>\", as in \"models\": {\"local/qwen3.8-27b\": {\"contextWindow\": 262144}}. Here it is %s, so none of it is in force.", f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "models.") {
		ref, ok := s.modelRef(m.line, m.key, "\"models\"")
		if !ok {
			continue
		}
		o := s.at(m.line)
		switch m.val.kind {
		case jNull:
			s.add(op{kind: KindRemoval, text: "models." + ref.String() + ": null (takes the model away)", origin: o,
				apply: func(b *builder) { b.removeModel(ref, o) }})
		case jObject:
			s.model(ref, m)
		default:
			s.errorf(m.line, "the model %s is an object with the fields %s (an empty one, {}, registers the model and says nothing more), or null to take away a model that a weaker file registered. Here it is %s.", ref, keyNames(modelFields), shown(m.val))
		}
	}
}

// model reads one registered model.
func (s *source) model(ref ModelRef, m jfield) {
	mark := len(s.probs.list)
	ch := modelChange{registered: s.at(m.line)}
	for _, fl := range s.unique(m.val, "models."+ref.String()+".") {
		key := "models." + ref.String() + "." + fl.key
		v := fl.val
		switch fl.key {
		case "name":
			switch v.kind {
			case jNull:
				ch.name = new(string)
			case jString:
				ch.name = &v.str
			default:
				s.errorf(fl.line, "%q is the name to show, as a string, and here it is %s.", key, shown(v))
			}
		case "contextWindow", "maxOutputTokens":
			n, whole := v.whole()
			switch {
			case v.kind == jNull && fl.key == "contextWindow":
				ch.noWindow = true
			case v.kind == jNull:
				ch.noMax = true
			case !whole || n < 1:
				s.errorf(fl.line, "%q is a number of tokens: a whole number of at least 1, written with digits alone. Here it is %s.", key, shown(v))
			case fl.key == "contextWindow":
				ch.window, ch.windowAt = &n, s.at(fl.line)
			default:
				ch.maxOutput, ch.maxAt = &n, s.at(fl.line)
			}
		case "thinkingBudget":
			n, whole := v.whole()
			switch {
			case v.kind == jNull:
				ch.noBudget = true
			case !whole || n < 0:
				s.errorf(fl.line, "%q is a number of tokens the model may think before a reply, 0 for no cap: a whole number written with digits alone. Here it is %s.", key, shown(v))
			default:
				ch.budget, ch.budgetAt = &n, s.at(fl.line)
			}
		case "thinkingLevels":
			items, isList := v.strings()
			switch {
			case v.kind == jNull:
				ch.noLevels = true
			case !isList:
				s.errorf(fl.line, "%q is the list of the model's thinking levels, the least thinking first, such as [\"off\", \"on\"] or [\"low\", \"medium\", \"high\"]; an empty list says the model has no thinking switch. Here it is %s.", key, emptyOr(v))
			default:
				ch.hasLevels, ch.levelsAt, ch.levels = true, s.at(fl.line), []string{}
				for _, it := range items {
					switch {
					case !levelPattern.MatchString(it.str):
						s.errorf(it.line, "%s is not a usable thinking level in %q: a level is a lower-case word such as off, on, low, medium or high.", showName(it.str), key)
					case slices.Contains(ch.levels, it.str):
						s.errorf(it.line, "the thinking level %s stands twice in %q.", it.str, key)
					default:
						ch.levels = append(ch.levels, it.str)
					}
				}
			}
		default:
			s.errorf(fl.line, "%s is not a field of a model. The fields are: %s.", showName(fl.key), keyNames(modelFields))
		}
	}
	if s.failed(mark) {
		return
	}
	o := ch.registered
	s.add(op{kind: KindModel, text: "model " + ref.String(), origin: o, apply: func(b *builder) { b.addModel(ref, ch, o) }})
}

// slotChange is what one file says of "model" or "smallModel". all is the
// whole key given as null: every choice a weaker file made for it is
// taken away.
type slotChange struct {
	all           bool
	ref           *ModelRef
	noRef         bool
	thinking      *string
	noThinking    bool
	temperature   *float64
	noTemperature bool
	// refAt, thinkingAt and temperatureAt are where each stands.
	refAt, thinkingAt, temperatureAt Origin
}

// slotShapes says the two shapes "model" and "smallModel" take, for a
// message about a value that has neither.
const slotShapes = `the model as a string, "<provider>/<model id>", or an object when how the model is asked matters, such as {"id": "<provider>/<model id>", "thinking": false, "temperature": 0.7}`

// slot reads "model" or "smallModel": the model as a string, or an object
// that also says how the model is asked.
func (s *source) slot(f jfield) {
	mark := len(s.probs.list)
	which, o := f.key, s.at(f.line)
	var ch slotChange
	text := which + ":"
	switch f.val.kind {
	case jNull:
		ch.all = true
		text += " null (takes the choice away)"
	case jString:
		ref, ok := s.modelRef(f.line, f.val.str, "\""+which+"\"")
		if !ok {
			return
		}
		ch.ref, ch.refAt = &ref, o
		text += " " + ref.String()
	case jObject:
		members := s.unique(f.val, which+".")
		if len(members) == 0 {
			s.errorf(f.line, "%q is an empty object, which says nothing. It is %s.", which, slotShapes)
			return
		}
		for _, fl := range members {
			key := which + "." + fl.key
			v := fl.val
			switch fl.key {
			case "id":
				switch v.kind {
				case jNull:
					ch.noRef = true
				case jString:
					if ref, ok := s.modelRef(fl.line, v.str, "\""+key+"\""); ok {
						ch.ref, ch.refAt = &ref, s.at(fl.line)
					}
				default:
					s.errorf(fl.line, "%q is the model, as the string \"<provider>/<model id>\", and here it is %s.", key, shown(v))
				}
			case "thinking":
				switch {
				case v.kind == jNull:
					ch.noThinking = true
				case v.kind == jBool:
					level := "off"
					if v.b {
						level = "on"
					}
					ch.thinking, ch.thinkingAt = &level, s.at(fl.line)
				case v.kind == jString && levelPattern.MatchString(v.str):
					ch.thinking, ch.thinkingAt = &v.str, s.at(fl.line)
				case v.kind == jString:
					s.errorf(fl.line, "%s is not a usable thinking level for %q: it is true, false, or a lower-case word such as \"low\", \"medium\" or \"high\".", showName(v.str), key)
				default:
					s.errorf(fl.line, "%q is true, false, or one of the model's own levels as a string, such as \"low\". Here it is %s.", key, shown(v))
				}
			case "temperature":
				t, err := strconv.ParseFloat(v.str, 64)
				switch {
				case v.kind == jNull:
					ch.noTemperature = true
				case v.kind != jNumber || err != nil || t < 0 || t > 2:
					s.errorf(fl.line, "%q is a number from 0 to 2, and here it is %s.", key, shown(v))
				default:
					ch.temperature, ch.temperatureAt = &t, s.at(fl.line)
				}
			default:
				s.errorf(fl.line, "%s is not a field of %q. The fields are: %s.", showName(fl.key), which, keyNames(slotFields))
			}
		}
		var parts []string
		if ch.ref != nil {
			parts = append(parts, ch.ref.String())
		}
		if ch.thinking != nil {
			parts = append(parts, "thinking "+*ch.thinking)
		}
		if ch.temperature != nil {
			parts = append(parts, "temperature "+strconv.FormatFloat(*ch.temperature, 'g', -1, 64))
		}
		if len(parts) == 0 {
			parts = append(parts, "takes away what a weaker file set")
		}
		text += " " + strings.Join(parts, ", ")
	default:
		s.errorf(f.line, "%q is %s. Here it is %s.", which, slotShapes, shown(f.val))
		return
	}
	if s.failed(mark) {
		return
	}
	s.add(op{kind: KindModel, text: text, origin: o, apply: func(b *builder) { b.setSlot(which, ch, o) }})
}

// MaxFallbacks is how many models "fallbackModel" may name, as in Claude
// Code.
const MaxFallbacks = 3

// fallbackShapes says the shapes "fallbackModel" takes, for a message.
const fallbackShapes = `one model as "<provider>/<model id>", or a list of up to three, tried in order`

// fallbackModel reads "fallbackModel": the models a turn goes on with when
// the session's model cannot answer. null, or an empty list, takes away
// what a weaker file named.
func (s *source) fallbackModel(f jfield) {
	mark := len(s.probs.list)
	o := s.at(f.line)
	var items []*jnode
	switch f.val.kind {
	case jNull:
	case jString:
		items = []*jnode{f.val}
	case jArray:
		items = f.val.items
	default:
		s.errorf(f.line, "\"fallbackModel\" is %s. Here it is %s.", fallbackShapes, shown(f.val))
		return
	}
	var refs []ModelRef
	for _, it := range items {
		if it.kind != jString {
			s.errorf(f.line, "\"fallbackModel\" is %s, and an item of it here is %s.", fallbackShapes, shown(it))
			continue
		}
		if ref, ok := s.modelRef(f.line, it.str, "\"fallbackModel\""); ok && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) > MaxFallbacks {
		s.errorf(f.line, "\"fallbackModel\" names %d models, and a turn falls back to %d at most. Keep the ones to try first.", len(refs), MaxFallbacks)
	}
	if s.failed(mark) {
		return
	}
	text := "fallbackModel: null (takes the fallback models away)"
	if len(refs) > 0 {
		names := make([]string, len(refs))
		for i, r := range refs {
			names[i] = r.String()
		}
		text = "fallbackModel: " + strings.Join(names, ", ")
	}
	s.add(op{kind: KindModel, text: text, origin: o, apply: func(b *builder) { b.fallback.put(refs, o) }})
}

// hosts reads the "hosts" object. Each declaration is checked by itself
// here, with the one check there is of a host (checkHosts), so that a
// mistake in a file that is held stops startup as it would in one that is
// in force; what only shows against the other declarations, a name
// declared twice, is found once every file has been read.
func (s *source) hosts(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"hosts\" is an object with one member for each machine, under the name the model calls it by, as in \"hosts\": {\"render\": {\"ssh\": \"root@203.0.113.10\", \"description\": \"the render box\"}}. Here it is %s, so none of it is in force.", f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "hosts.") {
		name, o := m.key, s.at(m.line)
		switch m.val.kind {
		case jNull:
			s.add(op{kind: KindRemoval, text: "hosts." + clip(name, 64) + ": null (takes the host away)", origin: o, feature: FeatureHosts, what: "taking a host away",
				apply: func(b *builder) { b.removeHost(name, o) }})
		case jObject:
			mark := len(s.probs.list)
			h := Host{Name: name, Origin: o}
			for _, fl := range s.unique(m.val, "hosts."+clip(name, 64)+".") {
				switch {
				case fl.key != "ssh" && fl.key != "description":
					s.errorf(fl.line, "%s is not a field of a host. The fields are: %s.", showName(fl.key), keyNames(hostFields))
				case fl.val.kind != jString:
					s.errorf(fl.line, "the %q of a host is a string, and here it is %s.", fl.key, shown(fl.val))
				case fl.key == "ssh":
					h.SSH = fl.val.str
				default:
					h.Description = fl.val.str
				}
			}
			if s.failed(mark) {
				continue
			}
			if wrong := checkHosts([]Host{h}); len(wrong) > 0 {
				s.probs.list = append(s.probs.list, wrong...)
				continue
			}
			s.add(op{kind: KindHost, text: "host " + h.Name + " → " + h.SSH, origin: o, feature: FeatureHosts, what: "a host",
				apply: func(b *builder) { b.addHost(h) }})
		default:
			s.errorf(m.line, "a host is an object with \"ssh\", the destination ssh is given, and if you like a \"description\"; or null, to take away a host that a weaker file declared. Here %s is %s.", showName(name), shown(m.val))
		}
	}
}

// options reads the "options" object.
func (s *source) options(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"options\" is an object of gnosh's own switches and limits, as in \"options\": {\"requestTimeout\": 900}. Here it is %s, so none of it is in force.", f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "options.") {
		def := findOption(m.key)
		if def == nil {
			s.errorf(m.line, "%s is not an option gnosh knows. The options are: %s.", showName(m.key), list(optionKeys()))
			continue
		}
		key, v, o := "options."+def.key, m.val, s.at(m.line)
		if def.ownOnly && (s.scope == "project" || s.scope == "local") {
			s.probs.warnf(s.file, m.line, "%q is not taken from a project's file, so that a repository cannot change how the box takes your keys. Set it in your own gnosh.json, with /config, or with --settings.", key)
			continue
		}
		if v.kind == jNull {
			// Putting an option back is never a setting that only takes
			// something away: it can bring back what a list had hidden.
			s.add(op{kind: KindOption, text: key + ": null (back to the default)", origin: o, apply: func(b *builder) { def.reset(&b.opts) }})
			continue
		}
		switch def.kind {
		case oBool:
			if v.kind != jBool {
				s.errorf(m.line, "%q is true or false, and here it is %s.", key, shown(v))
				continue
			}
			on := v.b
			s.add(op{kind: def.trustKind(), text: key + ": " + strconv.FormatBool(on), origin: o, apply: func(b *builder) { def.truth(&b.opts).put(on, o) }})
		case oInt:
			n, whole := v.whole()
			if !whole || def.bounded && (n < def.min || n > def.max) {
				switch {
				case def.bounded && def.max < 1<<30:
					s.errorf(m.line, "%q is a whole number from %d to %d, written with digits alone, and here it is %s.", key, def.min, def.max, shown(v))
				case def.bounded:
					s.errorf(m.line, "%q is a whole number of at least %d, written with digits alone, and here it is %s.", key, def.min, shown(v))
				default:
					s.errorf(m.line, "%q is a whole number, written with digits alone, and here it is %s.", key, shown(v))
				}
				continue
			}
			s.add(op{kind: def.trustKind(), text: key + ": " + strconv.Itoa(n), origin: o, apply: func(b *builder) { def.num(&b.opts).put(n, o) }})
		case oEnum:
			if v.kind != jString || !slices.Contains(def.enum, v.str) {
				got := shown(v)
				if v.kind == jString {
					got = showName(v.str)
				}
				s.errorf(m.line, "%q is one of \"%s\", and here it is %s.", key, strings.Join(def.enum, "\", \""), got)
				continue
			}
			word := v.str
			s.add(op{kind: def.trustKind(), text: key + ": " + word, origin: o, apply: func(b *builder) { def.text(&b.opts).put(word, o) }})
		case oURL:
			if v.kind != jString {
				s.errorf(m.line, "%q is an address as a string, such as \"http://127.0.0.1:8091/v1\", and here it is %s.", key, shown(v))
				continue
			}
			address := v.str
			s.secrets.addURL(address)
			if why := checkURL(address); why != "" {
				hint := ""
				if strings.Contains(address, "${") {
					hint = " (Variables are filled in only inside \"providers\".)"
				}
				s.errorf(m.line, "the address given in %q cannot be used: %s.%s", key, why, hint)
				continue
			}
			s.add(op{kind: def.trustKind(), text: key + ": " + DisplayURL(address), origin: o, apply: func(b *builder) { def.text(&b.opts).put(address, o) }})
		case oListen:
			if v.kind != jString {
				s.errorf(m.line, "%q is an address of this machine with a port, as a string, such as %q, or %q; here it is %s.", key, DefaultDeliverablesServer, ServerOff, shown(v))
				continue
			}
			where, wrong := checkListen(v.str)
			if wrong != "" {
				s.errorf(m.line, "the address given in %q cannot be used: %s.", key, wrong)
				continue
			}
			s.add(op{kind: def.trustKind(), text: key + ": " + where, origin: o, apply: func(b *builder) { def.text(&b.opts).put(where, o) }})
		case oName:
			if v.kind != jString || strings.TrimSpace(v.str) == "" || !printable(v.str) {
				s.errorf(m.line, "%q is a name as a string that is not empty and holds no control character, and here it is %s.", key, emptyString(v))
				continue
			}
			name := strings.TrimSpace(v.str)
			s.add(op{kind: def.trustKind(), text: key + ": " + name, origin: o, apply: func(b *builder) { def.text(&b.opts).put(name, o) }})
		case oPath:
			if v.kind != jString || v.str == "" || !printable(v.str) {
				s.errorf(m.line, "%q is a path as a string that is not empty and holds no control character, and here it is %s.", key, emptyString(v))
				continue
			}
			path := expandHome(v.str, s.home)
			s.add(op{kind: def.trustKind(), text: key + ": " + path, origin: o, apply: func(b *builder) { def.text(&b.opts).put(path, o) }})
		case oPathList, oNameList:
			what := "names"
			if def.kind == oPathList {
				what = "paths"
			}
			items, isList := v.strings()
			if !isList {
				s.errorf(m.line, "%q is a list of %s, each a string, and here it is %s.", key, what, emptyOr(v))
				continue
			}
			for _, it := range items {
				if it.str == "" || !printable(it.str) {
					s.errorf(it.line, "an entry of %q is empty, or holds a control character.", key)
					continue
				}
				if def.each != nil {
					if why := def.each(it.str); why != "" {
						s.errorf(it.line, "the entry %s of %q is not one gnosh can use: an entry %s.", showName(it.str), key, why)
						continue
					}
				}
				value, at := it.str, s.at(it.line)
				if def.kind == oPathList {
					base := s.baseDir
					if def.fromCwd {
						base = s.cwd
					}
					value = absPath(value, s.home, base)
				}
				s.add(op{kind: def.trustKind(), text: key + ": " + value, origin: at, apply: func(b *builder) { addItem(def.list(&b.opts), value, at) }})
			}
		}
	}
}

// emptyString is shown for a string that was expected to hold a usable
// value.
func emptyString(n *jnode) string {
	switch {
	case n.kind == jString && n.str == "":
		return "an empty string"
	case n.kind == jString:
		return "a string with a control character in it"
	}
	return shown(n)
}

// printable reports whether a string holds no control character. What a
// config file names (a model, a path, a skill) is shown in messages and in
// the listing of the configuration, and a file must not be able to write
// to the terminal through its own values.
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
