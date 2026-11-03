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
		`["case", ["==", ["get", "tunnel"], 0], ["literal", [1, 0]], ["literal", [2, 1]]]`)
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
	assertJSON(t, "filter", l["filter"], `["all", ["!=", ["get", "tunnel"], 1],
		["match", ["get", "type"], ["tertiary", "secondary", "primary"], true, false]]`)
	assertJSON(t, "line-width", l["paint"].(map[string]interface{})["line-width"],
		`["case", ["!=", ["get", "tunnel"], 1],
			["match", ["get", "type"], ["tertiary", "secondary"], 2, 3], 3]`)
}

func TestFilterOperators(t *testing.T) {
	_, style := build(t, `
		#water[area>40000][type!='marina'] { polygon-fill: blue; }
	`, "water")

	l := layerByID(t, style, "water::::polygon")
	assertJSON(t, "filter", l["filter"], `["all",
		["all", ["==", ["typeof", ["get", "area"]], "number"], [">", ["get", "area"], 40000]],
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
