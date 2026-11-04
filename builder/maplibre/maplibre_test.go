package maplibre

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omniscale/magnacarto/builder"
	"github.com/omniscale/magnacarto/color"
	"github.com/omniscale/magnacarto/mml"
)

func testOptions() Options {
	return Options{
		ZoomOffset: -1,
		Source: func(l mml.Layer) (string, string) {
			return "src", l.ID
		},
		Sources: map[string]interface{}{
			"src": map[string]interface{}{"type": "vector", "url": "{{MARTIN}}/src"},
		},
	}
}

// build converts the MSS for the given layer IDs and returns the decoded style.
func build(t *testing.T, mss string, layerIDs ...string) (*Map, map[string]interface{}) {
	t.Helper()
	return buildMap(t, testOptions(), mss, layerIDs...)
}

// buildWith is build with other options.
func buildWith(t *testing.T, opts Options, mss string, layerIDs ...string) map[string]interface{} {
	t.Helper()
	_, style := buildMap(t, opts, mss, layerIDs...)
	return style
}

func buildMap(t *testing.T, opts Options, mss string, layerIDs ...string) (*Map, map[string]interface{}) {
	t.Helper()
	m := New(opts)
	mmlObj := &mml.MML{}
	for _, id := range layerIDs {
		mmlObj.Layers = append(mmlObj.Layers, mml.Layer{ID: id, Active: true})
	}
	if err := builder.BuildMapFromString(m, mmlObj, mss); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var style map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &style); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	return m, style
}

func layers(style map[string]interface{}) []map[string]interface{} {
	var result []map[string]interface{}
	for _, l := range style["layers"].([]interface{}) {
		result = append(result, l.(map[string]interface{}))
	}
	return result
}

func layerByID(t *testing.T, style map[string]interface{}, id string) map[string]interface{} {
	t.Helper()
	for _, l := range layers(style) {
		if l["id"] == id {
			return l
		}
	}
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	t.Fatalf("layer %q not found, have %v", id, ids)
	return nil
}

func assertJSON(t *testing.T, what string, got interface{}, want string) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var w interface{}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation %s: %v", want, err)
	}
	ws, _ := json.Marshal(w)
	if string(g) != string(ws) {
		t.Errorf("%s:\n got  %s\n want %s", what, g, ws)
	}
}

func TestPolygonLayer(t *testing.T) {
	m, style := build(t, `
		Map { background-color: #8dc5e9; }
		#land[zoom>=11] { polygon-fill: #fdfdee; polygon-gamma: 0.75; }
	`, "land")

	if style["version"].(float64) != 8 {
		t.Errorf("version %v", style["version"])
	}
	assertJSON(t, "sources", style["sources"], `{"src":{"type":"vector","url":"{{MARTIN}}/src"}}`)

	all := layers(style)
	if len(all) != 2 || all[0]["id"] != "background" {
		t.Fatalf("expected background plus one layer, got %v", all)
	}
	assertJSON(t, "background paint", all[0]["paint"], `{"background-color":"#8dc5e9"}`)

	l := layerByID(t, style, "land::::polygon")
	if l["type"] != "fill" || l["source"] != "src" || l["source-layer"] != "land" {
		t.Errorf("unexpected layer header %v", l)
	}
	if l["minzoom"].(float64) != 10 {
		t.Errorf("minzoom %v, want 10 (Mapnik zoom 11 with offset -1)", l["minzoom"])
	}
	if _, ok := l["maxzoom"]; ok {
		t.Errorf("unexpected maxzoom %v for an open-ended zoom range", l["maxzoom"])
	}
	if _, ok := l["filter"]; ok {
		t.Errorf("unexpected filter %v", l["filter"])
	}
	assertJSON(t, "paint", l["paint"], `{"fill-color":"#fdfdee"}`)

	notes := m.Notes()
	if len(notes) != 1 || notes[0].Property != "polygon-gamma" || notes[0].Kind != Omitted || notes[0].Rules != 1 {
		t.Errorf("expected one omitted polygon-gamma note, got %+v", notes)
	}
}

// Rules of equal specificity come in reverse source order from magnacarto
// (the later rule wins), which the expressions keep.
func TestLineLayerByZoomAndFilter(t *testing.T) {
	_, style := build(t, `
		#roads[zoom=13] {
			[type='primary'] { line-width: 3.5; line-color: #ffee00; }
			[type='service'] { line-width: 0.5; line-color: #ffffff; }
		}
		#roads[zoom=14] {
			[type='primary'] { line-width: 7.5; line-color: #ffee00; }
		}
	`, "roads")

	l := layerByID(t, style, "roads::::line")
	if l["type"] != "line" {
		t.Errorf("type %v", l["type"])
	}
	if l["minzoom"].(float64) != 12 || l["maxzoom"].(float64) != 14 {
		t.Errorf("zoom range %v-%v, want 12-14", l["minzoom"], l["maxzoom"])
	}
	assertJSON(t, "filter", l["filter"], `["step", ["zoom"],
		["match", ["get", "type"], ["service", "primary"], true, false],
		13, ["==", ["get", "type"], "primary"]]`)
	assertJSON(t, "line-width", l["paint"].(map[string]interface{})["line-width"], `["step", ["zoom"],
		["case", ["==", ["get", "type"], "service"], 0.5, 3.5],
		13, 7.5]`)
	assertJSON(t, "line-color", l["paint"].(map[string]interface{})["line-color"], `["step", ["zoom"],
		["case", ["==", ["get", "type"], "service"], "#ffffff", "#ffee00"],
		13, "#ffee00"]`)
}

// Mapnik styles use filter-mode="first": a feature that matches an earlier
// rule without a drawing line symbolizer (here: width 0) must not be drawn by
// a later rule.
func TestZeroLineWidthDoesNotDraw(t *testing.T) {
	_, style := build(t, `
		#roads[type='path'] { line-width: 0; }
		#roads { line-width: 2; }
	`, "roads")

	l := layerByID(t, style, "roads::::line")
	assertJSON(t, "filter", l["filter"], `["case", ["==", ["get", "type"], "path"], false, true]`)
}

func TestAttachmentsAndClassesKeepMapnikOrder(t *testing.T) {
	_, style := build(t, `
		#roads {
			::case { line-width: 4; line-color: #888; }
			::fill { line-width: 2; line-color: #fff; line-cap: round; }
		}
	`, "roads")

	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	assertJSON(t, "layer order", ids, `["roads::case::line", "roads::fill::line"]`)
	assertJSON(t, "layout", layerByID(t, style, "roads::fill::line")["layout"], `{"line-cap":"round"}`)
}

func TestDasharrayInLineWidths(t *testing.T) {
	_, style := build(t, `
		#roads {
			[tunnel=1] { line-width: 2; line-dasharray: 4, 2; }
			[tunnel=0] { line-width: 4; }
		}
	`, "roads")

	paint := layerByID(t, style, "roads::::line")["paint"].(map[string]interface{})
	assertJSON(t, "line-dasharray", paint["line-dasharray"],
		`["case", ["any", ["==", ["get", "tunnel"], 0], ["==", ["get", "tunnel"], false]], ["literal", [1, 0]], ["literal", [2, 1]]]`)
}

// MapLibre GL JS (6.15) draws nothing for a data-driven line-dasharray whose
// pattern is shorter than one line width; repeating it looks the same.
func TestShortDashPatternIsRepeated(t *testing.T) {
	_, style := build(t, `#roads { line-width: 10; line-dasharray: 3, 1; }`, "roads")

	paint := layerByID(t, style, "roads::::line")["paint"].(map[string]interface{})
	assertJSON(t, "line-dasharray", paint["line-dasharray"], `["literal", [0.3, 0.1, 0.3, 0.1, 0.3, 0.1]]`)
}

// Rules that differ only in the value of one field, the usual
// [tunnel!=1][type='...'] pattern, become a match expression.
func TestMatchOnCommonField(t *testing.T) {
	_, style := build(t, `
		#roads[tunnel!=1] {
			[type='primary'] { line-width: 3; }
			[type='secondary'] { line-width: 2; }
			[type='tertiary'] { line-width: 2; }
		}
	`, "roads")

	l := layerByID(t, style, "roads::::line")
	assertJSON(t, "filter", l["filter"], `["all", ["!=", ["get", "tunnel"], 1], ["!=", ["get", "tunnel"], true],
		["match", ["get", "type"], ["tertiary", "secondary", "primary"], true, false]]`)
	assertJSON(t, "line-width", l["paint"].(map[string]interface{})["line-width"],
		`["case", ["all", ["!=", ["get", "tunnel"], 1], ["!=", ["get", "tunnel"], true]],
			["match", ["get", "type"], ["tertiary", "secondary"], 2, 3], 3]`)
}

func TestFilterOperators(t *testing.T) {
	_, style := build(t, `
		#water[area>40000][type!='marina'] { polygon-fill: blue; }
	`, "water")

	l := layerByID(t, style, "water::::polygon")
	assertJSON(t, "filter", l["filter"], `["all",
		["==", ["typeof", ["get", "area"]], "number"], [">", ["get", "area"], 40000],
		["!=", ["get", "type"], "marina"]]`)
}

func TestLayerInfo(t *testing.T) {
	m, _ := build(t, `
		#roads[zoom>=13][type='primary'][bridge!=1] { line-width: 2; }
		#empty[zoom>=13] { line-cap: round; }
	`, "roads", "empty")

	infos := m.Layers()
	if len(infos) != 1 {
		t.Fatalf("expected info for the one drawn layer, got %+v", infos)
	}
	info := infos[0]
	if info.Layer.ID != "roads" || info.MinZoom != 12 || info.MaxZoom != 21 {
		t.Errorf("unexpected info %+v", info)
	}
	assertJSON(t, "fields", info.Fields, `["bridge", "type"]`)
}

func TestUnsupportedSymbolizerIsReported(t *testing.T) {
	m, style := build(t, `
		#labels { dot-fill: #ff0000; dot-width: 4; }
	`, "labels")

	if len(layers(style)) != 0 {
		t.Errorf("expected no layers, got %v", layers(style))
	}
	notes := m.Notes()
	if len(notes) != 1 || notes[0].Property != "dot-" || notes[0].Kind != Omitted {
		t.Errorf("expected an omitted dot symbolizer note, got %+v", notes)
	}
}

func TestNonIntegerNumbersStayCase(t *testing.T) {
	_, style := build(t, `
		#roads { [width=1.5] { line-width: 1; } [width=2.5] { line-width: 2; } }
	`, "roads")

	f := layerByID(t, style, "roads::::line")["filter"]
	assertJSON(t, "filter", f, `["any", ["==", ["get", "width"], 2.5], ["==", ["get", "width"], 1.5]]`)
}

func TestUnsupportedFilterCountedOncePerRule(t *testing.T) {
	m, _ := build(t, `
		#roads[name=~'^A'] { line-width: 1; polygon-fill: red; }
	`, "roads")

	var found bool
	for _, n := range m.Notes() {
		if strings.HasPrefix(n.Property, "filter [name") {
			found = true
			if n.Rules != 1 {
				t.Errorf("rules %d, want 1", n.Rules)
			}
		}
	}
	if !found {
		t.Errorf("missing note for the regex filter: %+v", m.Notes())
	}
}

// Mapnik compares booleans as 0 and 1, e.g. [tunnel=1] matches a boolean
// tunnel attribute that is true.
func TestBooleanAttributesCompareAsZeroAndOne(t *testing.T) {
	_, style := build(t, `
		#tunnels[tunnel=1] { line-width: 2; }
		#bridges[bridge!=0] { line-width: 3; }
	`, "tunnels", "bridges")

	assertJSON(t, "tunnels", layerByID(t, style, "tunnels::::line")["filter"],
		`["any", ["==", ["get", "tunnel"], 1], ["==", ["get", "tunnel"], true]]`)
	assertJSON(t, "bridges", layerByID(t, style, "bridges::::line")["filter"],
		`["all", ["!=", ["get", "bridge"], 0], ["!=", ["get", "bridge"], false]]`)
}

func TestRasterLayer(t *testing.T) {
	m, style := build(t, `
		#relief[zoom>=1][zoom<=15] {
			raster-scaling: bilinear;
			raster-comp-op: multiply;
			raster-mesh-size: 1;
			[zoom=14] { raster-opacity: 0.5; }
			[zoom=15] { raster-opacity: 0.2; }
		}
	`, "relief")

	l := layerByID(t, style, "relief::::raster")
	if l["type"] != "raster" || l["source"] != "src" {
		t.Errorf("unexpected layer header %v", l)
	}
	if _, ok := l["source-layer"]; ok {
		t.Errorf("raster layer with source-layer %v", l["source-layer"])
	}
	assertJSON(t, "zoom range", []interface{}{l["minzoom"], l["maxzoom"]}, `[null, 15]`)
	assertJSON(t, "paint", l["paint"], `{
		"raster-opacity": ["step", ["zoom"], 1, 13, 0.5, 14, 0.2],
		"raster-resampling": "linear"
	}`)

	var omitted []string
	for _, n := range m.Notes() {
		if n.Kind == Omitted {
			omitted = append(omitted, n.Property)
		}
	}
	assertJSON(t, "omitted", omitted, `["raster-comp-op", "raster-mesh-size"]`)
}

func TestZeroRasterOpacityDoesNotDraw(t *testing.T) {
	_, style := build(t, `#relief[zoom>=11] { raster-opacity: 0; }`, "relief")
	if len(layers(style)) != 0 {
		t.Errorf("expected no layers, got %v", layers(style))
	}
}

func TestRewrite(t *testing.T) {
	opts := testOptions()
	var m *Map
	opts.Rewrite = func(l mml.Layer, layer Layer) []Layer {
		switch l.ID {
		case "drop":
			m.AddNote(l.ID, "", "raster-", Omitted, "dropped")
			return nil
		case "relief":
			layer.Type = "hillshade"
			layer.Paint = map[string]interface{}{"hillshade-exaggeration": 0.5}
			return []Layer{layer}
		}
		return []Layer{layer}
	}
	m = New(opts)
	mmlObj := &mml.MML{Layers: []mml.Layer{{ID: "relief", Active: true}, {ID: "drop", Active: true}}}
	if err := builder.BuildMapFromString(m, mmlObj, `#relief, #drop { raster-opacity: 0.8; }`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var style map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &style); err != nil {
		t.Fatal(err)
	}
	all := layers(style)
	if len(all) != 1 || all[0]["type"] != "hillshade" {
		t.Fatalf("expected one hillshade layer, got %v", all)
	}
	assertJSON(t, "paint", all[0]["paint"], `{"hillshade-exaggeration": 0.5}`)
	if infos := m.Layers(); len(infos) != 1 || infos[0].Layer.ID != "relief" {
		t.Errorf("expected layer info for relief only, got %+v", infos)
	}
	if n := m.Notes(); len(n) != 1 || n[0].Layer != "drop" || n[0].Detail != "dropped" {
		t.Errorf("expected the note of the hook, got %+v", n)
	}
}

func notesOf(m *Map, kind NoteKind) []string {
	var result []string
	for _, n := range m.Notes() {
		if n.Kind == kind {
			result = append(result, n.Property)
		}
	}
	return result
}

func TestPolygonPatternLayer(t *testing.T) {
	m, style := build(t, `
		#wetland[zoom>=11] {
			polygon-pattern-file: url(Pattern/wetland_v02.svg);
			polygon-pattern-opacity: 0.7;
			polygon-pattern-comp-op: multiply;
		}
	`, "wetland")

	l := layerByID(t, style, "wetland::::polygon-pattern")
	if l["type"] != "fill" || l["source"] != "src" || l["source-layer"] != "wetland" {
		t.Errorf("unexpected layer header %v", l)
	}
	assertJSON(t, "paint", l["paint"], `{"fill-opacity": 0.7, "fill-pattern": "Pattern/wetland_v02"}`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `["polygon-pattern-comp-op"]`)
}

func TestPolygonPatternByFilter(t *testing.T) {
	_, style := build(t, `
		#overlay {
			[type='nature_reserve'] { polygon-pattern-file: url('Pattern/nature_reserve.svg'); }
			[type='military'] { polygon-pattern-file: url("Pattern/military.png"); }
		}
	`, "overlay")

	l := layerByID(t, style, "overlay::::polygon-pattern")
	assertJSON(t, "filter", l["filter"], `["match", ["get", "type"], ["military", "nature_reserve"], true, false]`)
	assertJSON(t, "paint", l["paint"], `{"fill-pattern": ["case", ["==", ["get", "type"], "military"], "Pattern/military", "Pattern/nature_reserve"]}`)
}

func TestBuildingLayer(t *testing.T) {
	m, style := build(t, `
		#buildings[zoom>=16] { building-fill: #e0d8c8; building-height: 1.25; }
	`, "buildings")

	all := layers(style)
	if len(all) != 2 || all[0]["id"] != "buildings::::building-walls" || all[1]["id"] != "buildings::::building" {
		t.Fatalf("expected walls below the roof, got %v", all)
	}
	for _, l := range all {
		if l["type"] != "fill" || l["source-layer"] != "buildings" || l["minzoom"].(float64) != 15 {
			t.Errorf("unexpected layer header %v", l)
		}
	}
	// Mapnik draws the walls in the fill colour times 0.8, truncated.
	assertJSON(t, "walls paint", all[0]["paint"], `{"fill-color": "#b3aca0"}`)
	assertJSON(t, "roof paint", all[1]["paint"], `{
		"fill-color": "#e0d8c8",
		"fill-translate": ["literal", [0, -1.25]],
		"fill-translate-anchor": "viewport"
	}`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["building-"]`)
	if infos := m.Layers(); len(infos) != 1 || infos[0].MinZoom != 15 {
		t.Errorf("unexpected layer info %+v", infos)
	}
}

func TestBuildingHeightByFeatureFallsBack(t *testing.T) {
	m, style := build(t, `
		#buildings {
			building-fill: #cccccc; building-height: 1;
			[type='tower'] { building-height: 3; }
		}
	`, "buildings")

	roof := layerByID(t, style, "buildings::::building")
	// fill-translate takes no data expressions: the fallback height for all.
	assertJSON(t, "translate", roof["paint"].(map[string]interface{})["fill-translate"], `["literal", [0, -1]]`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["building-", "building-height"]`)
	if n := m.Notes(); n[len(n)-1].Rules != 1 {
		t.Errorf("building-height reported %d times, want once", n[len(n)-1].Rules)
	}
}

func TestPatternPathIsCleaned(t *testing.T) {
	_, style := build(t, `#w { polygon-pattern-file: url(./Pattern/wetland.svg); }`, "w")
	l := layerByID(t, style, "w::::polygon-pattern")
	assertJSON(t, "paint", l["paint"], `{"fill-pattern": "Pattern/wetland"}`)
}

// Options.Blend replaces the colour and opacity of the rules with a
// line-comp-op it approximates; other rules keep theirs, and the report says
// approximated instead of omitted.
func TestBlendApproximatesLineCompOp(t *testing.T) {
	opts := testOptions()
	opts.Blend = func(op string, c color.Color, opacity float64) (color.Color, float64, string, bool) {
		if op != "darken" {
			return c, opacity, "", false
		}
		return color.MustParse("#808080"), opacity * 0.5, "darken as opacity", true
	}
	m := New(opts)
	mmlObj := &mml.MML{Layers: []mml.Layer{{ID: "roads", Active: true}}}
	err := builder.BuildMapFromString(m, mmlObj, `
		#roads {
			[type='road'] { line-width: 1; line-color: #dddcdc; line-comp-op: darken; }
			[type='track'] { line-width: 1; line-color: #dddcdc; line-comp-op: multiply; }
			[type='primary'] { line-width: 2; line-color: #ff0000; }
		}`)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var style map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &style); err != nil {
		t.Fatal(err)
	}
	paint := layerByID(t, style, "roads::::line")["paint"].(map[string]interface{})
	// Rules of equal specificity come in reverse source order.
	assertJSON(t, "line-color", paint["line-color"], `["match", ["get", "type"], "primary", "#ff0000", "track", "#dddcdc", "#808080"]`)
	assertJSON(t, "line-opacity", paint["line-opacity"], `["match", ["get", "type"], ["primary", "track"], 1, 0.5]`)

	var got []string
	for _, n := range m.Notes() {
		got = append(got, n.Property+" "+string(n.Kind)+" "+n.Detail)
	}
	want := "line-comp-op omitted MapLibre has no compositing operations|line-comp-op approximated darken as opacity"
	if g := strings.Join(got, "|"); g != want {
		t.Errorf("notes\n got  %s\n want %s", g, want)
	}
}

func TestTextLayer(t *testing.T) {
	m, style := build(t, `
		#places[zoom>=10] {
			text-name: '[name]';
			text-face-name: 'Fira Sans Bold', 'Unifont Medium';
			text-size: 10;
			text-fill: #333333;
			text-halo-radius: 1.5;
			text-wrap-width: 50;
			text-character-spacing: 2;
			text-transform: uppercase;
			text-repeat-distance: 100;
			[zoom>=12] { text-size: 20; }
		}
	`, "places")

	l := layerByID(t, style, "places::::text")
	if l["type"] != "symbol" || l["source-layer"] != "places" || l["minzoom"].(float64) != 9 {
		t.Errorf("unexpected layer header %v", l)
	}
	assertJSON(t, "layout", l["layout"], `{
		"symbol-z-order": "source",
		"text-field": ["get", "name"],
		"text-font": ["literal", ["Fira Sans Bold", "Unifont Medium"]],
		"text-justify": "auto",
		"text-letter-spacing": ["step", ["zoom"], 0.2, 11, 0.1],
		"text-max-width": ["step", ["zoom"], 5, 11, 2.5],
		"text-padding": 0,
		"text-size": ["step", ["zoom"], 10, 11, 20],
		"text-transform": "uppercase"
	}`)
	assertJSON(t, "paint", l["paint"], `{
		"text-color": "#333333",
		"text-halo-color": "#ffffff",
		"text-halo-width": 1.5
	}`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `["text-repeat-distance"]`)
}

func TestTextWithoutWrapWidthDoesNotWrap(t *testing.T) {
	_, style := build(t, `#places { text-name: [name]; text-face-name: 'Arial'; text-size: 10; }`, "places")
	l := layerByID(t, style, "places::::text")
	assertJSON(t, "text-max-width", l["layout"].(map[string]interface{})["text-max-width"], `1000`)
}

func TestTextNeedsNameAndSize(t *testing.T) {
	_, style := build(t, `
		#places {
			text-name: [name]; text-face-name: 'Arial'; text-size: 10;
			[type='hidden'] { text-name: ''; }
			[type='nosize'] { text-size: 0; }
		}
	`, "places")
	l := layerByID(t, style, "places::::text")
	assertJSON(t, "filter", l["filter"], `["match", ["get", "type"], ["nosize", "hidden"], false, true]`)
}

func TestTextNameConcatenatesFieldsAndLiterals(t *testing.T) {
	_, style := build(t, `
		#a { text-name: '('+'[name]'+')'; text-face-name: 'Arial'; text-size: 10; }
		#b { text-name: "Taxi"; text-face-name: 'Arial'; text-size: 10; }
		#c { text-name: "[ref] [name]"; text-face-name: 'Arial'; text-size: 10; }
	`, "a", "b", "c")
	assertJSON(t, "a", layerByID(t, style, "a::::text")["layout"].(map[string]interface{})["text-field"],
		`["concat", "(", ["get", "name"], ")"]`)
	assertJSON(t, "b", layerByID(t, style, "b::::text")["layout"].(map[string]interface{})["text-field"], `"Taxi"`)
	assertJSON(t, "c", layerByID(t, style, "c::::text")["layout"].(map[string]interface{})["text-field"],
		`["concat", ["get", "ref"], " ", ["get", "name"]]`)
}

func TestTextDisplacementBecomesAnchor(t *testing.T) {
	_, style := build(t, `
		#places {
			text-name: [name]; text-face-name: 'Arial'; text-size: 10;
			[type='a'] { text-dx: 5; text-dy: -10; }
			[type='b'] { text-dy: 5; text-horizontal-alignment: middle; text-vertical-alignment: middle; }
		}
	`, "places")
	l := layerByID(t, style, "places::::text")
	assertJSON(t, "anchor", l["layout"].(map[string]interface{})["text-variable-anchor-offset"], `["match", ["get", "type"],
		"b", ["literal", ["center", [0, 0.5]]],
		"a", ["literal", ["bottom-left", [0.5, -1]]],
		["literal", ["center", [0, 0]]]]`)
}

func TestTextPlacementsBecomeVariableAnchors(t *testing.T) {
	m, style := build(t, `
		#places {
			text-name: [name]; text-face-name: 'Arial'; text-size: 10;
			text-placement-type: simple; text-placements: "N,E,SW,9,8";
			text-dx: 4; text-dy: 6; text-justify-alignment: center;
		}
	`, "places")
	l := layerByID(t, style, "places::::text")
	layout := l["layout"].(map[string]interface{})
	assertJSON(t, "anchor", layout["text-variable-anchor-offset"],
		`["literal", ["bottom", [0, -0.6], "left", [0.4, 0], "top-right", [-0.4, 0.6]]]`)
	assertJSON(t, "justify", layout["text-justify"], `"center"`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["text-placements"]`)
}

func TestLineLabel(t *testing.T) {
	_, style := build(t, `
		#roads { text-name: [name]; text-face-name: 'Arial'; text-size: 10;
			text-placement: line; text-dy: 5; text-spacing: 300; text-max-char-angle-delta: 30; }
	`, "roads")
	l := layerByID(t, style, "roads::::text")
	layout := l["layout"].(map[string]interface{})
	assertJSON(t, "placement", layout["symbol-placement"], `"line"`)
	assertJSON(t, "spacing", layout["symbol-spacing"], `300`)
	assertJSON(t, "max-angle", layout["text-max-angle"], `30`)
	assertJSON(t, "offset", layout["text-offset"], `["literal", [0, 0.5]]`)
	if _, ok := layout["text-variable-anchor-offset"]; ok {
		t.Errorf("unexpected anchor for line placement: %v", layout["text-variable-anchor-offset"])
	}
}

func TestSymbolLayersComeLastInReverseOrder(t *testing.T) {
	_, style := build(t, `
		#roads { line-width: 1; }
		#blocker { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-opacity: 0; }
		#labels { text-name: [name]; text-face-name: 'Arial'; text-size: 10; }
		#borders { line-width: 2; }
	`, "roads", "blocker", "labels", "borders")
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	assertJSON(t, "order", ids, `["roads::::line", "borders::::line", "labels::::text", "blocker::::text"]`)
	assertJSON(t, "blocker paint", layerByID(t, style, "blocker::::text")["paint"], `{"text-opacity": 0}`)
}

func TestMatchHoistedOutOfDifferingConditions(t *testing.T) {
	_, style := build(t, `
		#land {
			[type='forest'], [type='wood'] { line-color: #00ff00; }
			[type='park'] { line-color: #00aa00; }
			line-width: 1;
			[area>=100] { line-width: 2; }
			[area>=10] { line-width: 3; }
		}
	`, "land")
	l := layerByID(t, style, "land::::line")
	// Without hoisting these are case chains over all type × area rules.
	// [area>=10] comes later with the same specificity and wins.
	assertJSON(t, "line-width", l["paint"].(map[string]interface{})["line-width"], `["case",
		["all", ["==", ["typeof", ["get", "area"]], "number"], [">=", ["get", "area"], 10]], 3,
		["all", ["==", ["typeof", ["get", "area"]], "number"], [">=", ["get", "area"], 100]], 2,
		1]`)
	assertJSON(t, "line-color", l["paint"].(map[string]interface{})["line-color"],
		`["match", ["get", "type"], "park", "#00aa00", ["wood", "forest"], "#00ff00", "#000000"]`)
}

func TestHoistedMatchKeepsFirstMatch(t *testing.T) {
	entries := []caseEntry{
		{[]interface{}{"all", []interface{}{"==", []interface{}{"get", "type"}, "a"}, []interface{}{">", []interface{}{"get", "n"}, 1.0}}, 1.0},
		{[]interface{}{">", []interface{}{"get", "n"}, 5.0}, 2.0},
		{[]interface{}{"all", []interface{}{"==", []interface{}{"get", "type"}, "b"}, []interface{}{">", []interface{}{"get", "n"}, 1.0}}, 3.0},
		{[]interface{}{"==", []interface{}{"get", "type"}, "a"}, 4.0},
	}
	got, ok := hoistedMatch(entries, 0.0)
	if !ok {
		t.Fatal("not hoisted")
	}
	assertJSON(t, "expr", got, `["match", ["get", "type"],
		"a", ["case", [">", ["get", "n"], 1], 1, [">", ["get", "n"], 5], 2, 4],
		"b", ["case", [">", ["get", "n"], 5], 2, [">", ["get", "n"], 1], 3, 0],
		["case", [">", ["get", "n"], 5], 2, 0]]`)
}

func TestDuplicateConjunctsAreDropped(t *testing.T) {
	_, style := build(t, `#land[area>=10][area<100] { line-width: 1; }`, "land")
	assertJSON(t, "filter", layerByID(t, style, "land::::line")["filter"], `["all",
		["==", ["typeof", ["get", "area"]], "number"], [">=", ["get", "area"], 10], ["<", ["get", "area"], 100]]`)
}

func TestPassesBecomeFurtherAnchors(t *testing.T) {
	m, style := build(t, `
		#places[zoom>=14] {
			::center, ::var1, ::var2 {
				text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 100;
				::var1, ::var2 { text-placement-type: simple; text-placements: "W,E"; }
				::var1 { text-dx: 4; }
				::var2 { text-dx: 8; [zoom>=16] { text-dx: 16; } }
			}
		}
	`, "places")
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	assertJSON(t, "layers", ids, `["places::center::text"]`)
	l := layerByID(t, style, "places::center::text")
	assertJSON(t, "anchors", l["layout"].(map[string]interface{})["text-variable-anchor-offset"], `["step", ["zoom"],
		["literal", ["center", [0, 0], "right", [-0.4, 0], "left", [0.4, 0], "right", [-0.8, 0], "left", [0.8, 0]]],
		15,
		["literal", ["center", [0, 0], "right", [-0.4, 0], "left", [0.4, 0], "right", [-1.6, 0], "left", [1.6, 0]]]]`)
	var merged []string
	for _, n := range m.Notes() {
		if n.Property == "text-" && n.Kind == Approximated {
			merged = append(merged, n.Attachment)
		}
	}
	assertJSON(t, "merged passes", merged, `["var1", "var2"]`)
}

func TestDifferentPassesAreNotMerged(t *testing.T) {
	_, style := build(t, `
		#places {
			::a { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 100; }
			::b { text-name: [ref]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 100; text-dx: 4; }
			::c { text-name: [ref]; text-face-name: 'Arial'; text-size: 10; text-dx: 8; }
		}
	`, "places")
	if n := len(layers(style)); n != 3 {
		t.Errorf("%d layers, want 3: other text, or no repeat distance", n)
	}
}

func TestPassWithPlacementExceptionAndOtherSymbolizers(t *testing.T) {
	_, style := build(t, `
		#places {
			::center, ::var1 {
				text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 50;
				line-width: 1;
			}
			::var1 { text-dx: 4; [name='Spain'] { text-dy: 12; } }
		}
	`, "places")
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	assertJSON(t, "layers", ids, `["places::center::line", "places::var1::line", "places::center::text"]`)
	assertJSON(t, "anchors", layerByID(t, style, "places::center::text")["layout"].(map[string]interface{})["text-variable-anchor-offset"],
		`["literal", ["center", [0, 0], "left", [0.4, 0]]]`)
}

func TestPassMayLackRulesWithoutText(t *testing.T) {
	_, style := build(t, `
		#places[zoom>=8] {
			::center, ::var1[zoom>=9] {
				line-width: 1;
				[zoom>=9] { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 50; }
			}
			::var1 { text-dx: 4; }
		}
	`, "places")
	if _, ok := layerByID(t, style, "places::center::text")["layout"].(map[string]interface{})["text-variable-anchor-offset"]; !ok {
		t.Error("var1 not merged")
	}
	for _, l := range layers(style) {
		if l["id"] == "places::var1::text" {
			t.Error("var1 not merged")
		}
	}
}

func TestLineLabelsAreSplitWhereTextSizeChanges(t *testing.T) {
	_, style := build(t, `
		#roads[zoom>=12] {
			text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-placement: line;
			[zoom>=14] { text-size: 12; [type='primary'] { text-size: 14; } }
			[zoom>=16] { text-size: 20; }
		}
		#places[zoom>=12] { text-name: [name]; text-face-name: 'Arial'; text-size: 10; [zoom>=14] { text-size: 12; } }
	`, "roads", "places")
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	// MapLibre fits line labels with text-size at zoom 18.
	assertJSON(t, "layers", ids, `["places::::text", "roads::::text#zoom=15", "roads::::text#zoom=13", "roads::::text#zoom=11"]`)
	for id, want := range map[string]string{
		"roads::::text#zoom=11": `[11, 13, 10]`,
		"roads::::text#zoom=13": `[13, 15, ["case", ["==", ["get", "type"], "primary"], 14, 12]]`,
		"roads::::text#zoom=15": `[15, null, ["case", ["==", ["get", "type"], "primary"], 14, 20]]`,
	} {
		l := layerByID(t, style, id)
		assertJSON(t, id, []interface{}{l["minzoom"], l["maxzoom"], l["layout"].(map[string]interface{})["text-size"]}, want)
	}
}

func TestPassesAreMergedPerZoom(t *testing.T) {
	_, style := build(t, `
		#places[zoom>=12] {
			::center, ::var1 {
				[zoom=12][weight>=100], [zoom>=13] {
					text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 50;
				}
			}
			::var1 { text-dx: 4; [zoom=12] { text-name: [name]; text-face-name: 'Arial'; text-size: 11; text-repeat-distance: 50; } }
		}
	`, "places")
	// At Mapnik zoom 12 var1 draws more features than center: it stays a
	// layer of its own there, and is merged from zoom 13.
	l := layerByID(t, style, "places::var1::text")
	if l["minzoom"].(float64) != 11 || l["maxzoom"].(float64) != 12 {
		t.Errorf("var1 at %v-%v, want only style zoom 11", l["minzoom"], l["maxzoom"])
	}
	assertJSON(t, "anchors", layerByID(t, style, "places::center::text")["layout"].(map[string]interface{})["text-variable-anchor-offset"],
		`["step", ["zoom"], ["literal", ["center", [0, 0]]], 12, ["literal", ["center", [0, 0], "left", [0.4, 0]]]]`)
}

func TestPassForOtherFeaturesIsNotMerged(t *testing.T) {
	_, style := build(t, `
		#places {
			::a[type='city'] { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 50; }
			::b[type='city'], ::b[type='town'] { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-repeat-distance: 50; text-dx: 4; }
		}
	`, "places")
	// ::b draws towns, which ::a does not.
	layerByID(t, style, "places::b::text")
}

func TestNonSimplePlacementTypeIsReported(t *testing.T) {
	m, _ := build(t, `#places { text-name: [name]; text-face-name: 'Arial'; text-size: 10; text-placement-type: list; }`, "places")
	assertJSON(t, "approximated", notesOf(m, Approximated), `["text-placement-type"]`)
}

func TestPointLayer(t *testing.T) {
	m, style := build(t, `
		#pois[zoom>=15] {
			[type='hospital'] { point-file: url(Security/hospital.svg); }
			[type='camp_site'] { point-file: url(Accommodation/camping.svg); point-allow-overlap: true; }
			point-placement: interior;
			[zoom=15] { point-transform: "scale(2.2)"; }
			[zoom>=16] { point-transform: "scale(2.5)"; }
		}
	`, "pois")
	l := layerByID(t, style, "pois::::point")
	if l["type"] != "symbol" || l["source-layer"] != "pois" || l["minzoom"].(float64) != 14 {
		t.Errorf("unexpected layer header %v", l)
	}
	assertJSON(t, "filter", l["filter"], `["match", ["get", "type"], ["camp_site", "hospital"], true, false]`)
	assertJSON(t, "layout", l["layout"], `{
		"icon-allow-overlap": false,
		"icon-image": ["case", ["==", ["get", "type"], "camp_site"], "Accommodation/camping", "Security/hospital"],
		"icon-padding": 0,
		"icon-size": ["step", ["zoom"], 2.2, 15, 2.5],
		"symbol-z-order": "source"
	}`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["point-allow-overlap"]`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `null`)
}

// Martin rounds sprite images to whole pixels; icon-size draws them at the
// size of the image file.
func TestImageSizeCorrectsIconSize(t *testing.T) {
	opts := testOptions()
	opts.ImageSize = func(image string) (float64, float64, bool) {
		if image == "a" {
			return 5, 4, true
		}
		return 0, 0, false
	}
	style := buildWith(t, opts, `
		#pois { point-file: url(a.svg); point-transform: "scale(2)"; [type='b'] { point-file: url(b.svg); } }
	`, "pois")
	l := layerByID(t, style, "pois::::point")
	assertJSON(t, "icon-size", l["layout"].(map[string]interface{})["icon-size"],
		`["case", ["==", ["get", "type"], "b"], 2, 2.5]`)
}

func TestShieldLayer(t *testing.T) {
	m, style := build(t, `
		#towns[zoom>=6] {
			shield-file: url(Town/circle.svg);
			shield-name: '[name]';
			shield-face-name: 'Arial';
			shield-fill: #333333;
			shield-text-transform: uppercase;
			shield-repeat-distance: 20;
			shield-unlock-image: true;
			shield-placement-type: simple; shield-placements: "NE,W";
			shield-size: 10; shield-wrap-width: 40; shield-transform: "scale(0.7)";
			shield-text-dx: 3; shield-text-dy: 4;
			[zoom>=7] { shield-size: 20; shield-transform: "scale(0.8)"; shield-margin: 2; }
		}
	`, "towns")
	l := layerByID(t, style, "towns::::shield")
	if l["type"] != "symbol" || l["minzoom"].(float64) != 5 {
		t.Errorf("unexpected layer header %v", l)
	}
	assertJSON(t, "layout", l["layout"], `{
		"icon-image": "Town/circle",
		"icon-padding": 0,
		"icon-rotation-alignment": "viewport",
		"icon-size": ["step", ["zoom"], 0.7, 6, 0.8],
		"symbol-z-order": "source",
		"text-field": ["get", "name"],
		"text-font": ["literal", ["Arial"]],
		"text-justify": "auto",
		"text-max-width": ["step", ["zoom"], 4, 6, 2],
		"text-padding": ["step", ["zoom"], 0, 6, 2],
		"text-rotation-alignment": "viewport",
		"text-size": ["step", ["zoom"], 10, 6, 20],
		"text-transform": "uppercase",
		"text-variable-anchor-offset": ["step", ["zoom"],
			["literal", ["bottom-left", [0.3, -0.4], "right", [-0.3, 0]]], 6,
			["literal", ["bottom-left", [0.15, -0.2], "right", [-0.15, 0]]]]
	}`)
	assertJSON(t, "paint", l["paint"], `{"text-color": "#333333"}`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `["shield-repeat-distance"]`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `null`)
}

// Mapnik keeps shields upright along lines; the image is drawn without text
// where the name is empty.
func TestLineShieldAndEmptyName(t *testing.T) {
	_, style := build(t, `
		#refs {
			shield-name: "[ref]"; shield-face-name: 'Arial'; shield-size: 9;
			shield-file: url(Highway_Shield/motorway.svg);
			shield-placement: line; shield-spacing: 40;
			[reflen>3] { shield-name: ""; shield-file: url(Highway_Shield/junction.svg); }
		}
	`, "refs")
	l := layerByID(t, style, "refs::::shield")
	layout := l["layout"].(map[string]interface{})
	if _, ok := l["filter"]; ok {
		t.Errorf("unexpected filter %v: the image is drawn without a name", l["filter"])
	}
	assertJSON(t, "placement", layout["symbol-placement"], `"line"`)
	assertJSON(t, "spacing", layout["symbol-spacing"], `40`)
	assertJSON(t, "rotation", layout["text-rotation-alignment"], `"viewport"`)
	assertJSON(t, "field", layout["text-field"],
		`["case", ["all", ["==", ["typeof", ["get", "reflen"]], "number"], [">", ["get", "reflen"], 3]], "", ["get", "ref"]]`)
}

func TestShieldPassesBecomeFurtherAnchors(t *testing.T) {
	m, style := build(t, `
		#towns {
			::center, ::var1 {
				shield-file: url(a.svg); shield-name: [name]; shield-face-name: 'Arial'; shield-size: 10;
				shield-repeat-distance: 20; shield-unlock-image: true;
				shield-placement-type: simple; shield-placements: "E,W";
			}
			::center { shield-text-dx: 2; }
			::var1 { shield-text-dx: 4; }
		}
	`, "towns")
	var ids []interface{}
	for _, l := range layers(style) {
		ids = append(ids, l["id"])
	}
	assertJSON(t, "layers", ids, `["towns::center::shield"]`)
	assertJSON(t, "anchors", layerByID(t, style, "towns::center::shield")["layout"].(map[string]interface{})["text-variable-anchor-offset"],
		`["literal", ["left", [0.2, 0], "right", [-0.2, 0], "left", [0.4, 0], "right", [-0.4, 0]]]`)
	var merged []string
	for _, n := range m.Notes() {
		if n.Property == "shield-" && n.Kind == Approximated {
			merged = append(merged, n.Attachment)
		}
	}
	assertJSON(t, "merged passes", merged, `["var1"]`)
}

// A pass with another image is not the same shield.
func TestShieldPassWithOtherImageIsNotMerged(t *testing.T) {
	_, style := build(t, `
		#towns {
			::center, ::var1 { shield-name: [name]; shield-face-name: 'Arial'; shield-size: 10; shield-repeat-distance: 20; }
			::center { shield-file: url(a.svg); }
			::var1 { shield-file: url(b.svg); shield-text-dx: 4; }
		}
	`, "towns")
	if n := len(layers(style)); n != 2 {
		t.Errorf("%d layers, want 2", n)
	}
}

func TestShieldSizeDefaultsToMapnik(t *testing.T) {
	_, style := build(t, `
		#junctions { shield-file: url(a.svg); shield-name: [ref]; shield-face-name: 'Arial'; [reflen=2] { shield-size: 8; } }
	`, "junctions")
	assertJSON(t, "text-size", layerByID(t, style, "junctions::::shield")["layout"].(map[string]interface{})["text-size"],
		`["case", ["==", ["get", "reflen"], 2], 8, 10]`)
}

func TestLineMarkerLayer(t *testing.T) {
	opts := testOptions()
	opts.ImageSize = func(image string) (float64, float64, bool) { return 6.8, 8, true }
	m, style := buildMap(t, opts, `
		#oneway[zoom>=17] {
			marker-file: url(Highway_Shield/oneway.svg);
			marker-placement: line;
			marker-max-error: 5;
			[zoom=17] { marker-width: 10; marker-spacing: 200; }
			[zoom>=18] { marker-width: 14; marker-spacing: 400; }
		}
	`, "oneway")
	l := layerByID(t, style, "oneway::::marker")
	assertJSON(t, "layout", l["layout"], `{
		"icon-image": "Highway_Shield/oneway",
		"icon-padding": 0,
		"icon-size": ["step", ["zoom"], 1.25, 17, 1.75],
		"symbol-placement": "line",
		"symbol-spacing": ["step", ["zoom"], 200, 17, 400],
		"symbol-z-order": "source"
	}`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `["marker-max-error"]`)
}

// Without the image size the marker is drawn at the size of its sprite
// image.
func TestMarkerWidthNeedsImageSize(t *testing.T) {
	m, style := build(t, `#a { marker-file: url(a.svg); marker-width: 10; }`, "a")
	l := layerByID(t, style, "a::::marker")
	assertJSON(t, "icon-size", l["layout"].(map[string]interface{})["icon-size"], `1`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["marker-width"]`)
}

func TestShieldWithoutSizeUsesDefaultForEms(t *testing.T) {
	_, style := build(t, `
		#j { shield-file: url(a.svg); shield-name: [ref]; shield-face-name: 'Arial'; shield-wrap-width: 20; shield-text-dx: 5;
			[reflen=2] { shield-size: 5; } }
	`, "j")
	layout := layerByID(t, style, "j::::shield")["layout"].(map[string]interface{})
	assertJSON(t, "max-width", layout["text-max-width"], `["case", ["==", ["get", "reflen"], 2], 4, 2]`)
}

// MapLibre multiplies icon-offset by icon-size, like a translate after a
// scale in an SVG transform list.
func TestPointTransformWithTranslate(t *testing.T) {
	m, style := build(t, `
		#detail {
			point-file: url(flag.svg);
			[type='a'] { point-transform: "scale(2.0),translate(1.5,-2.5)"; }
			[type='b'] { point-transform: "translate(4, 2) scale(2)"; }
		}
	`, "detail")
	layout := layerByID(t, style, "detail::::point")["layout"].(map[string]interface{})
	assertJSON(t, "icon-size", layout["icon-size"], `["match", ["get", "type"], ["b", "a"], 2, 1]`)
	assertJSON(t, "icon-offset", layout["icon-offset"],
		`["match", ["get", "type"], "b", ["literal", [2, 1]], "a", ["literal", [1.5, -2.5]], ["literal", [0, 0]]]`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `null`)
}

func TestMarkerTranslateIsReported(t *testing.T) {
	m, _ := build(t, `#a { marker-file: url(a.svg); marker-transform: "translate(1,2)"; }`, "a")
	assertJSON(t, "approximated", notesOf(m, Approximated), `["marker-transform"]`)
}

// Mapnik breaks the line at \n in the text.
func TestTextNameNewline(t *testing.T) {
	_, style := build(t, `#peaks { text-name: [name]+'\n'+[ele]+'m'; text-face-name: 'Arial'; text-size: 10; }`, "peaks")
	assertJSON(t, "text-field", layerByID(t, style, "peaks::::text")["layout"].(map[string]interface{})["text-field"],
		`["concat", ["get", "name"], "\n", ["get", "ele"], "m"]`)
}

// Along a line, Mapnik repeats a shield no closer than its repeat distance.
func TestLineShieldSpacingKeepsRepeatDistance(t *testing.T) {
	m, style := build(t, `
		#refs[zoom>=8] {
			shield-name: "[ref]"; shield-face-name: 'Arial'; shield-size: 9; shield-file: url(a.svg);
			shield-placement: line; shield-spacing: 40;
			[zoom=8] { shield-repeat-distance: 120; }
			[zoom>=9] { shield-repeat-distance: 20; }
		}
	`, "refs")
	layout := layerByID(t, style, "refs::::shield")["layout"].(map[string]interface{})
	assertJSON(t, "spacing", layout["symbol-spacing"], `["step", ["zoom"], 120, 8, 40]`)
	assertJSON(t, "approximated", notesOf(m, Approximated), `["shield-repeat-distance"]`)
	assertJSON(t, "omitted", notesOf(m, Omitted), `null`)
}

// Icons can come from another sprite than patterns, e.g. one with images
// in a higher resolution.
func TestIconsFromOtherSprite(t *testing.T) {
	opts := testOptions()
	opts.Sprites = []Sprite{{ID: "default", URL: "{{MARTIN}}/sprite/all"}, {ID: "x4", URL: "{{MARTIN}}/sprite/all_x4"}}
	opts.IconImage = func(image string) string { return "x4:" + image }
	opts.ImageSize = func(image string) (float64, float64, bool) {
		if image == "Town/circle" {
			return 5, 20, true
		}
		return 0, 0, false
	}
	style := buildWith(t, opts, `
		#towns { shield-file: url(Town/circle.svg); shield-name: [name]; shield-face-name: 'Arial'; shield-size: 10; shield-transform: "scale(2)"; }
		#pois { point-file: url(Town/circle.svg); }
		#wetland { polygon-pattern-file: url(Pattern/wetland.svg); }
	`, "towns", "pois", "wetland")
	assertJSON(t, "sprite", style["sprite"],
		`[{"id": "default", "url": "{{MARTIN}}/sprite/all"}, {"id": "x4", "url": "{{MARTIN}}/sprite/all_x4"}]`)
	shield := layerByID(t, style, "towns::::shield")["layout"].(map[string]interface{})
	assertJSON(t, "shield image", shield["icon-image"], `"x4:Town/circle"`)
	assertJSON(t, "shield size", shield["icon-size"], `0.5`)
	point := layerByID(t, style, "pois::::point")["layout"].(map[string]interface{})
	assertJSON(t, "point image", point["icon-image"], `"x4:Town/circle"`)
	assertJSON(t, "point size", point["icon-size"], `0.25`)
	assertJSON(t, "pattern", layerByID(t, style, "wetland::::polygon-pattern")["paint"].(map[string]interface{})["fill-pattern"],
		`"Pattern/wetland"`)
}

func TestZeroScaleHasNoOffset(t *testing.T) {
	_, style := build(t, `#a { point-file: url(a.svg); point-transform: "translate(2,2) scale(0)"; }`, "a")
	layout := layerByID(t, style, "a::::point")["layout"].(map[string]interface{})
	if _, ok := layout["icon-offset"]; ok {
		t.Errorf("unexpected icon-offset %v", layout["icon-offset"])
	}
}

// Mapnik's defaults apply where no rule sets them.
func TestIconDefaults(t *testing.T) {
	_, style := build(t, `
		#m { marker-file: url(a.svg); marker-placement: line; }
		#s { shield-file: url(a.svg); shield-name: [ref]; shield-face-name: 'Arial'; }
	`, "m", "s")
	assertJSON(t, "marker spacing", layerByID(t, style, "m::::marker")["layout"].(map[string]interface{})["symbol-spacing"], `100`)
	assertJSON(t, "shield size", layerByID(t, style, "s::::shield")["layout"].(map[string]interface{})["text-size"], `10`)
}
