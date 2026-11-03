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
	// Sources is written as the style's "sources".
	Sources map[string]interface{}
	// Name and Metadata are written to the style as is.
	Name     string
	Metadata map[string]interface{}
	// Glyphs and Sprite are written to the style if set.
	Glyphs string
	Sprite string
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
}

// Map collects the converted layers. It implements builder.Map.
type Map struct {
	opts      Options
	bgColor   *color.Color
	layers    []styleLayer
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
	minZoom, maxZoom := -1, -1

	for _, group := range groupByAttachment(rules) {
		m.noteUnsupported(l.ID, group)
		for _, r := range group.rules {
			group.conds = append(group.conds, m.condition(l.ID, group.name, r.Filters))
		}
		for _, sym := range symbolizersOf(group.rules) {
			sl, ok := m.convertSymbolizer(l.ID, group, sym)
			if !ok {
				continue
			}
			sl.Source = source
			sl.SourceLayer = sourceLayer
			m.layers = append(m.layers, sl.styleLayer)
			for _, f := range sl.fields {
				fields[f] = true
			}
			if minZoom < 0 || sl.first < minZoom {
				minZoom = sl.first
			}
			if sl.last > maxZoom {
				maxZoom = sl.last
			}
		}
	}
	if minZoom < 0 {
		return
	}
	info := LayerInfo{Layer: l, MinZoom: minZoom, MaxZoom: maxZoom, Fields: []string{}}
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
			for _, k := range r.Properties.Keys() {
				if !strings.HasPrefix(k, p.Name) || isOtherPrefix(k, p.Name) {
					continue
				}
				if _, ok := conv.props[k]; ok {
					continue
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
		Sprite:   m.opts.Sprite,
		Layers:   []styleLayer{},
	}
	if s.Sources == nil {
		s.Sources = map[string]interface{}{}
	}
	if m.bgColor != nil {
		s.Layers = append(s.Layers, styleLayer{
			ID:    "background",
			Type:  "background",
			Paint: map[string]interface{}{"background-color": fmtColor(*m.bgColor)},
		})
	}
	s.Layers = append(s.Layers, m.layers...)
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
	layers := make([]styleLayer, len(s.Layers))
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
	Sprite   string                 `json:"sprite,omitempty"`
	Glyphs   string                 `json:"glyphs,omitempty"`
	Layers   []styleLayer           `json:"layers"`
}

type styleLayer struct {
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
