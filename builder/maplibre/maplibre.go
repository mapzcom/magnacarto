// Package maplibre builds MapLibre style JSON from CartoCSS.
//
// Every MML layer becomes one MapLibre layer per attachment and symbolizer
// (ID "<mml-layer-id>::<attachment>::<prefix>", e.g. "roads::case::line"), in
// the order Mapnik draws them. The rules of an attachment keep Mapnik's
// filter-mode="first" semantics: filter and paint/layout values are "case"
// expressions over the rules in order, wrapped in a "step" over the zoom
// where the rules differ between zoom levels.
//
// Data sources are not derived from the MML datasources. The caller maps each
// MML layer to a MapLibre source and source-layer with Options.Source and
// provides the style's sources, e.g. a vector tile server that serves one
// source-layer per MML layer. Layers returns what such a server needs to know:
// the zoom range and the feature attributes each layer is read with.
//
// Pattern files are referenced as sprite images named by their path without
// extension (url(Pattern/wetland.svg) → "Pattern/wetland"); Options.Sprite
// must provide them. Buildings are drawn flat: walls below, the roof moved
// up by the building height.
//
// Point, shield and marker images become symbol layers with icons from the
// same sprite images (or the ones Options.IconImage names); icon-size is the
// scale of *-transform corrected by Options.ImageSize. Shields draw icon
// and text in one layer, the text converted like text-.
//
// Raster symbolizers become "raster" layers without a source-layer. Options.Rewrite
// lets the caller change converted layers, e.g. draw a raster from a DEM
// source as "hillshade" or "color-relief".
//
// MapLibre has no compositing operations. Options.Blend lets the caller
// approximate line-comp-op with a different colour and opacity, e.g. for a
// known backdrop.
//
// Features without an equivalent in MapLibre are recorded as Notes rather
// than silently dropped.
package maplibre

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/omniscale/magnacarto/color"
	"github.com/omniscale/magnacarto/mml"
	"github.com/omniscale/magnacarto/mss"
)

// Options configure a Map.
type Options struct {
	// ZoomOffset is added to Mapnik zoom levels to get MapLibre zoom levels.
	// MapLibre with 512 px tiles shows at zoom z what Mapnik with 256 px tiles
	// shows at zoom z+1, hence -1 (the default from DefaultOptions).
	ZoomOffset int
	// MaxMapnikZoom is the highest Mapnik zoom level rules are evaluated for.
	// Layers that are still visible there have no maxzoom.
	MaxMapnikZoom int
	// Source returns the MapLibre source ID and source-layer for an MML layer.
	Source func(l mml.Layer) (source, sourceLayer string)
	// Rewrite, if set, replaces each converted layer with the returned
	// layers (none to drop it), e.g. to draw a raster from a DEM source as
	// "hillshade" or "color-relief". Use Map.AddNote to report the change.
	Rewrite func(l mml.Layer, layer Layer) []Layer
	// Blend, if set, approximates the compositing operation of a line
	// (line-comp-op), which MapLibre does not have: it gets the operation
	// and the colour and opacity of a rule and returns the colour and
	// opacity to draw with instead, plus the reason for the report. With ok
	// false the operation is omitted.
	Blend func(op string, c color.Color, opacity float64) (blended color.Color, blendedOpacity float64, detail string, ok bool)
	// ImageSize, if set, returns the width of an image as Mapnik draws it
	// (e.g. the width of the SVG) and as MapLibre draws the sprite image
	// (in CSS pixels), for the sprite image name (see imageValue). Icons are
	// scaled by their ratio, and marker-width is converted with it; without
	// it both widths are taken as equal.
	ImageSize func(image string) (native, sprite float64, ok bool)
	// Sources is written as the style's "sources".
	Sources map[string]interface{}
	// Name and Metadata are written to the style as is.
	Name     string
	Metadata map[string]interface{}
	// Glyphs and Sprite are written to the style if set.
	Glyphs string
	Sprite string
	// Sprites, if set, are written as the style's list of sprites instead
	// of Sprite.
	Sprites []Sprite
	// IconImage, if set, returns the name under which the style references
	// the image of an icon (point-, shield-, marker-), e.g. in another
	// sprite ("x4:Town/circle"). Options.ImageSize then gives the width of
	// that sprite image. Pattern images are not changed.
	IconImage func(image string) string
	// WrapText, if set, returns the feature attribute that holds a text
	// wrapped as Mapnik wraps it (text-wrap-width, shield-wrap-width), for
	// a text that Mapnik wraps. The label then reads that attribute and
	// MapLibre does not wrap it again (text-max-width): MapLibre balances the
	// lines below the width and does not wrap line labels at all. With ok
	// false MapLibre wraps the text. LayerInfo.FieldZooms says in which zoom
	// levels the data needs the attribute.
	WrapText func(w TextWrap) (field string, ok bool)
}

// Sprite is one sprite of a style's list of sprites.
type Sprite struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// DefaultOptions returns Options for 512 px vector tiles and Mapnik zoom
// levels up to 22.
func DefaultOptions() Options {
	return Options{ZoomOffset: -1, MaxMapnikZoom: 22}
}

// NoteKind tells how the converter treated a feature without an exact
// MapLibre equivalent.
type NoteKind string

const (
	// Approximated features are converted with a visible difference.
	Approximated NoteKind = "approximated"
	// Omitted features are left out of the style.
	Omitted NoteKind = "omitted"
)

// Note records a feature that was approximated or omitted.
type Note struct {
	Layer      string // MML layer ID
	Attachment string
	// Property is a CartoCSS property (e.g. "polygon-gamma") or, for a whole
	// symbolizer, its prefix (e.g. "text-").
	Property string
	Kind     NoteKind
	Detail   string
	Rules    int // number of rules with this feature
}

// LayerInfo describes how the style uses the data of an MML layer.
type LayerInfo struct {
	Layer mml.Layer
	// MinZoom and MaxZoom are the MapLibre zoom levels (inclusive) in which
	// at least one style layer draws features of this MML layer.
	MinZoom, MaxZoom int
	// Fields are the sorted feature attributes the style reads.
	Fields []string
	// FieldZooms are the lowest and highest MapLibre zoom level in which
	// the style reads each of the Fields.
	FieldZooms map[string][2]int
}

// Map collects the converted layers. It implements builder.Map.
type Map struct {
	opts      Options
	bgColor   *color.Color
	layers    []Layer
	infos     []LayerInfo
	notes     []Note
	noteIndex map[noteKey]int
}

type noteKey struct {
	layer, attachment, property string
	kind                        NoteKind
}

// New returns an empty Map. Zero values of ZoomOffset and MaxMapnikZoom in
// opts are taken as is; start from DefaultOptions to get the usual values.
func New(opts Options) *Map {
	if opts.MaxMapnikZoom == 0 {
		opts.MaxMapnikZoom = 22
	}
	return &Map{opts: opts, noteIndex: map[noteKey]int{}}
}

// SetBackgroundColor implements builder.MapOptionsSetter.
func (m *Map) SetBackgroundColor(c color.Color) {
	m.bgColor = &c
}

// Layers returns one LayerInfo per MML layer that draws anything, in
// drawing order.
func (m *Map) Layers() []LayerInfo {
	return m.infos
}

// Notes returns all approximated and omitted features in the order found.
func (m *Map) Notes() []Note {
	return m.notes
}

// UnsupportedFeatures implements builder.Map.
func (m *Map) UnsupportedFeatures() []string {
	var result []string
	for _, n := range m.notes {
		result = append(result, n.Layer+"::"+n.Attachment+" "+n.Property+" "+string(n.Kind))
	}
	return result
}

// AddNote records an approximated or omitted feature of an MML layer; notes
// with the same layer, attachment, property and kind are counted as one.
func (m *Map) AddNote(layer, attachment, property string, kind NoteKind, detail string) {
	m.note(layer, attachment, property, kind, detail)
}

func (m *Map) note(layer, attachment, property string, kind NoteKind, detail string) {
	k := noteKey{layer, attachment, property, kind}
	if i, ok := m.noteIndex[k]; ok {
		m.notes[i].Rules++
		return
	}
	m.noteIndex[k] = len(m.notes)
	m.notes = append(m.notes, Note{Layer: layer, Attachment: attachment, Property: property, Kind: kind, Detail: detail, Rules: 1})
}

// AddLayer implements builder.Map.
func (m *Map) AddLayer(l mml.Layer, rules []mss.Rule) {
	source, sourceLayer := "", l.ID
	if m.opts.Source != nil {
		source, sourceLayer = m.opts.Source(l)
	}

	fields := map[string]bool{}
	fieldZooms := map[string][2]int{}
	minZoom, maxZoom := -1, -1

	groups := groupByAttachment(rules)
	for i := range groups {
		m.noteUnsupported(l.ID, groups[i])
		for _, r := range groups[i].rules {
			groups[i].conds = append(groups[i].conds, m.condition(l.ID, groups[i].name, r.Filters))
		}
	}
	for _, group := range m.mergePasses(l.ID, groups) {
		for _, sym := range symbolizersOf(group.rules) {
			for _, part := range symbolizerParts(sym.Name) {
				sl, ok := m.convertSymbolizer(l.ID, group, sym, part.conv, part.suffix)
				if !ok {
					continue
				}
				sl.Source = source
				if sl.Type != "raster" {
					sl.SourceLayer = sourceLayer
				}
				var out []Layer
				for _, part := range splitLineLabels(sl.Layer) {
					if m.opts.Rewrite != nil {
						out = append(out, m.opts.Rewrite(l, part)...)
					} else {
						out = append(out, part)
					}
				}
				if len(out) == 0 {
					continue
				}
				m.layers = append(m.layers, out...)
				for _, f := range sl.fields {
					fields[f] = true
				}
				for f, zr := range sl.fieldZooms {
					addZooms(fieldZooms, f, zr)
				}
				if minZoom < 0 || sl.first < minZoom {
					minZoom = sl.first
				}
				if sl.last > maxZoom {
					maxZoom = sl.last
				}
			}
		}
	}
	if minZoom < 0 {
		return
	}
	info := LayerInfo{Layer: l, MinZoom: minZoom, MaxZoom: maxZoom, Fields: []string{}, FieldZooms: fieldZooms}
	for f := range fields {
		info.Fields = append(info.Fields, f)
	}
	sort.Strings(info.Fields)
	m.infos = append(m.infos, info)
}

type attachmentGroup struct {
	name  string
	rules []mss.Rule
	conds []interface{} // MapLibre condition per rule
	// passes are following attachments whose label is merged into this
	// one, per label symbolizer prefix, see mergePasses; mergedZooms are the
	// Mapnik zoom levels at which the label of such a pass is merged.
	passes      map[string][]attachmentGroup
	mergedZooms map[string]map[int]bool
}

// groupByAttachment groups rules by attachment in order of first occurrence,
// which is the order in which Mapnik draws the styles of a layer.
func groupByAttachment(rules []mss.Rule) []attachmentGroup {
	var groups []attachmentGroup
	index := map[string]int{}
	for _, r := range rules {
		i, ok := index[r.Attachment]
		if !ok {
			i = len(groups)
			index[r.Attachment] = i
			groups = append(groups, attachmentGroup{name: r.Attachment})
		}
		groups[i].rules = append(groups[i].rules, r)
	}
	return groups
}

type symbolizerPart struct {
	conv   symConverter
	suffix string // appended to the layer ID
}

// symbolizerParts returns the converters of the MapLibre layers a
// symbolizer becomes, bottom first; none if it is not supported.
func symbolizerParts(name string) []symbolizerPart {
	conv, ok := converters[name]
	if !ok {
		return nil
	}
	var parts []symbolizerPart
	if conv.underlay != nil {
		parts = append(parts, symbolizerPart{*conv.underlay, conv.underlaySuffix})
	}
	return append(parts, symbolizerPart{conv, ""})
}

// symbolizersOf returns the symbolizers used by the rules in order of first
// occurrence.
func symbolizersOf(rules []mss.Rule) []mss.Prefix {
	var result []mss.Prefix
	seen := map[mss.Prefix]bool{}
	for _, r := range rules {
		for _, p := range mss.SortedPrefixes(r.Properties, symbolizerPrefixes) {
			if !seen[p] {
				seen[p] = true
				result = append(result, p)
			}
		}
	}
	return result
}

// noteUnsupported records properties that no symbolizer converts.
func (m *Map) noteUnsupported(layer string, group attachmentGroup) {
	for _, r := range group.rules {
		for _, p := range mss.SortedPrefixes(r.Properties, symbolizerPrefixes) {
			r.Properties.SetDefaultInstance(p.Instance)
			conv, ok := converters[p.Name]
			if !ok {
				m.note(layer, group.name, instanceName(p.Instance, p.Name), Omitted, "symbolizer not supported")
				continue
			}
			if !conv.draws(r.Properties) {
				continue
			}
			if conv.approximated != "" {
				m.note(layer, group.name, instanceName(p.Instance, p.Name), Approximated, conv.approximated)
			}
			for _, k := range r.Properties.Keys() {
				if !strings.HasPrefix(k, p.Name) || isOtherPrefix(k, p.Name) {
					continue
				}
				if approx, ok := conv.approximate[k]; ok {
					if detail, ok := approx(m, r.Properties); ok {
						m.note(layer, group.name, instanceName(p.Instance, k), Approximated, detail)
						continue
					}
				}
				if _, ok := conv.props[k]; ok || conv.handles(k) {
					continue
				}
				if conv.blend != nil && k == conv.blend.compOp {
					if _, detail, ok := m.blended(conv, r.Properties); ok {
						m.note(layer, group.name, instanceName(p.Instance, k), Approximated, detail)
						continue
					}
				}
				detail := "no MapLibre equivalent"
				if d, ok := conv.omitted[k]; ok {
					detail = d
				}
				m.note(layer, group.name, instanceName(p.Instance, k), Omitted, detail)
			}
		}
		r.Properties.SetDefaultInstance("")
		for _, k := range r.Properties.Keys() {
			if !hasAnyPrefix(k, symbolizerPrefixes) {
				m.note(layer, group.name, k, Omitted, "style-level property not supported")
			}
		}
	}
}

func instanceName(instance, name string) string {
	if instance == "" {
		return name
	}
	return instance + "/" + name
}

func hasAnyPrefix(k string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// isOtherPrefix reports whether k belongs to a longer symbolizer prefix than
// prefix, e.g. "line-pattern-file" for "line-".
func isOtherPrefix(k, prefix string) bool {
	for _, p := range symbolizerPrefixes {
		if len(p) > len(prefix) && strings.HasPrefix(p, prefix) && strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// Write writes the style as indented JSON.
func (m *Map) Write(w io.Writer) error {
	s := style{
		Version:  8,
		Name:     m.opts.Name,
		Metadata: m.opts.Metadata,
		Sources:  m.opts.Sources,
		Glyphs:   m.opts.Glyphs,
		Layers:   []Layer{},
	}
	if len(m.opts.Sprites) > 0 {
		s.Sprite = m.opts.Sprites
	} else if m.opts.Sprite != "" {
		s.Sprite = m.opts.Sprite
	}
	if s.Sources == nil {
		s.Sources = map[string]interface{}{}
	}
	if m.bgColor != nil {
		s.Layers = append(s.Layers, Layer{
			ID:    "background",
			Type:  "background",
			Paint: map[string]interface{}{"background-color": fmtColor(*m.bgColor)},
		})
	}
	// MapLibre places the symbols of the top layer first, Mapnik those of
	// the first layer: symbol layers come last, in reverse order, to keep
	// Mapnik's priorities and collision blockers.
	var symbols []Layer
	for _, l := range m.layers {
		if l.Type == "symbol" {
			symbols = append(symbols, l)
		} else {
			s.Layers = append(s.Layers, l)
		}
	}
	for i := len(symbols) - 1; i >= 0; i-- {
		s.Layers = append(s.Layers, symbols[i])
	}
	return writeStyle(w, s)
}

// writeStyle writes the style indented, but each filter and property value
// on one line: expressions get long, and one line per value keeps the file
// small and diffs readable.
func writeStyle(w io.Writer, s style) error {
	var exprs [][]byte
	placeholder := func(v interface{}) (interface{}, error) {
		b, err := marshal(v, "")
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, b)
		return fmt.Sprintf("\x00%d\x00", len(exprs)-1), nil
	}
	layers := make([]Layer, len(s.Layers))
	for i, l := range s.Layers {
		var err error
		if l.Filter != nil {
			if l.Filter, err = placeholder(l.Filter); err != nil {
				return err
			}
		}
		for _, props := range []*map[string]interface{}{&l.Layout, &l.Paint} {
			if *props == nil {
				continue
			}
			compact := map[string]interface{}{}
			for k, v := range *props {
				if compact[k], err = placeholder(v); err != nil {
					return err
				}
			}
			*props = compact
		}
		layers[i] = l
	}
	s.Layers = layers

	out, err := marshal(s, "  ")
	if err != nil {
		return err
	}
	out = placeholderRe.ReplaceAllFunc(out, func(ph []byte) []byte {
		i, _ := strconv.Atoi(string(placeholderRe.FindSubmatch(ph)[1]))
		return exprs[i]
	})
	_, err = w.Write(out)
	return err
}

// placeholderRe matches the JSON encoding of the placeholders in writeStyle.
var placeholderRe = regexp.MustCompile(`"\\u0000(\d+)\\u0000"`)

func marshal(v interface{}, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if indent == "" {
		return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
	}
	return buf.Bytes(), nil
}

// WriteFiles writes the style to the file basename.
func (m *Map) WriteFiles(basename string) error {
	f, err := os.Create(basename)
	if err != nil {
		return err
	}
	if err := m.Write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type style struct {
	Version  int                    `json:"version"`
	Name     string                 `json:"name,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Sources  map[string]interface{} `json:"sources"`
	Sprite   interface{}            `json:"sprite,omitempty"`
	Glyphs   string                 `json:"glyphs,omitempty"`
	Layers   []Layer                `json:"layers"`
}

// Layer is a MapLibre style layer.
type Layer struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Source      string                 `json:"source,omitempty"`
	SourceLayer string                 `json:"source-layer,omitempty"`
	MinZoom     *int                   `json:"minzoom,omitempty"`
	MaxZoom     *int                   `json:"maxzoom,omitempty"`
	Filter      interface{}            `json:"filter,omitempty"`
	Layout      map[string]interface{} `json:"layout,omitempty"`
	Paint       map[string]interface{} `json:"paint,omitempty"`
}

func fmtColor(c color.Color) string {
	return c.String()
}
