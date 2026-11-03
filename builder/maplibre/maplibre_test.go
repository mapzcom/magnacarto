package maplibre

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omniscale/magnacarto/builder"
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
	m := New(testOptions())
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
		#labels { text-name: [name]; text-face-name: 'Arial'; text-size: 10; }
	`, "labels")

	if len(layers(style)) != 0 {
		t.Errorf("expected no layers, got %v", layers(style))
	}
	notes := m.Notes()
	if len(notes) != 1 || notes[0].Property != "text-" || notes[0].Kind != Omitted {
		t.Errorf("expected an omitted text symbolizer note, got %+v", notes)
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
