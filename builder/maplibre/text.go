package maplibre

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/omniscale/magnacarto/mss"
)

// MapLibre measures text layout in ems, Mapnik in pixels; per rule the
// pixels are divided by text-size.

// noWrap is the text-max-width (in ems) for Mapnik's default of no wrapping.
const noWrap = 1000.0

// defaultLineHeight is MapLibre's default text-line-height in ems, taken as
// the font's line height that Mapnik adds text-line-spacing to.
const defaultLineHeight = 1.2

var textConverter = symConverter{
	layerType: "symbol",
	draws: func(p *mss.Properties) bool {
		size, ok := p.GetFloat("text-size")
		if !ok || size <= 0 {
			return false
		}
		field, ok := textFieldValue(p)
		return ok && field != ""
	},
	props: map[string]propConv{
		"text-name":      {target: "text-field", layout: true, value: textFieldValue},
		"text-face-name": {target: "text-font", layout: true, value: fontValue},
		"text-size":      {target: "text-size", layout: true, value: floatValue("text-size", 1)},
		"text-wrap-width": {target: "text-max-width", layout: true, always: true, def: noWrap,
			value: emValue("text-wrap-width", 0, true)},
		"text-character-spacing": {target: "text-letter-spacing", layout: true, def: 0.0,
			value: emValue("text-character-spacing", 0, false)},
		"text-line-spacing": {target: "text-line-height", layout: true, zoomOnly: true, def: defaultLineHeight,
			value: emValue("text-line-spacing", defaultLineHeight, false)},
		"text-transform":         {target: "text-transform", layout: true, def: "none", value: transformValue},
		"text-justify-alignment": {target: "text-justify", layout: true, always: true, def: "auto", value: justifyValue},
		// Mapnik keeps no space around labels unless text-margin is set,
		// MapLibre 2 px by default.
		"text-margin":               {target: "text-padding", layout: true, zoomOnly: true, always: true, def: 0.0, value: floatValue("text-margin", 1)},
		"text-placement":            {target: "symbol-placement", layout: true, zoomOnly: true, def: "point", value: placementValue},
		"text-spacing":              {target: "symbol-spacing", layout: true, zoomOnly: true, def: 250.0, value: positiveValue("text-spacing")},
		"text-allow-overlap":        {target: "text-allow-overlap", layout: true, zoomOnly: true, def: false, value: boolValue("text-allow-overlap")},
		"text-max-char-angle-delta": {target: "text-max-angle", layout: true, zoomOnly: true, def: 22.5, value: floatValue("text-max-char-angle-delta", 1)},
		// Point labels: placements, displacement and alignment become anchors
		// with offsets; line labels are displaced with text-offset.
		"text-placements": {target: "text-variable-anchor-offset", layout: true, def: literal([]interface{}{"center", []float64{0, 0}}), value: anchorValue},
		"text-dx":         {target: "text-offset", layout: true, def: literal([]float64{0, 0}), value: lineOffsetValue},

		"text-fill":        {target: "text-color", value: colorValue("text-fill"), def: "#000000"},
		"text-opacity":     {target: "text-opacity", value: floatValue("text-opacity", 1), def: 1.0},
		"text-halo-fill":   {target: "text-halo-color", value: haloColorValue, def: "#ffffff"},
		"text-halo-radius": {target: "text-halo-width", value: floatValue("text-halo-radius", 1), def: 0.0},
	},
	handled: []string{"text-dy", "text-placement-type", "text-horizontal-alignment", "text-vertical-alignment"},
	approximate: map[string]func(p *mss.Properties) (string, bool){
		"text-placement-type": func(p *mss.Properties) (string, bool) {
			if t, ok := p.GetString("text-placement-type"); ok && t != "simple" && t != "dummy" {
				return "only simple placements are converted, the label keeps its first position", true
			}
			return "", false
		},
		"text-placements": func(p *mss.Properties) (string, bool) {
			if _, sizes := placementDirections(p); sizes {
				return "only the first text size: MapLibre has no smaller fallback sizes", true
			}
			return "", false
		},
	},
	omitted: map[string]string{
		"text-repeat-distance":          "MapLibre has no minimum distance between labels of different features",
		"text-label-position-tolerance": "MapLibre places line labels at fixed anchors",
		"text-halo-rasterizer":          "MapLibre renders halos itself",
		"text-comp-op":                  "MapLibre has no compositing operations",
		"text-halo-comp-op":             "MapLibre has no compositing operations",
	},
	constLayout: map[string]interface{}{"symbol-z-order": "source"},
}

var fieldRe = regexp.MustCompile(`\[([^\]]+)\]`)

// textFieldValue converts text-name, a template with [field] placeholders,
// to a string or an expression.
func textFieldValue(p *mss.Properties) (interface{}, bool) {
	vals, ok := p.GetFieldList("text-name")
	if !ok {
		return nil, false
	}
	var parts []interface{}
	addLiteral := func(s string) {
		if s == "" {
			return
		}
		if n := len(parts); n > 0 {
			if prev, ok := parts[n-1].(string); ok {
				parts[n-1] = prev + s
				return
			}
		}
		parts = append(parts, s)
	}
	for _, v := range vals {
		var tmpl string
		switch v := v.(type) {
		case mss.Field:
			tmpl = string(v)
		case string:
			tmpl = v
		default:
			continue
		}
		pos := 0
		for _, m := range fieldRe.FindAllStringSubmatchIndex(tmpl, -1) {
			addLiteral(tmpl[pos:m[0]])
			parts = append(parts, []interface{}{"get", tmpl[m[2]:m[3]]})
			pos = m[1]
		}
		addLiteral(tmpl[pos:])
	}
	switch len(parts) {
	case 0:
		return "", true
	case 1:
		return parts[0], true
	}
	return append([]interface{}{"concat"}, parts...), true
}

func fontValue(p *mss.Properties) (interface{}, bool) {
	faces, ok := p.GetStringList("text-face-name")
	if !ok || len(faces) == 0 {
		return nil, false
	}
	return literal(faces), true
}

// emValue returns base plus the pixel property in ems; with positive only
// values above zero count as set.
func emValue(name string, base float64, positive bool) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		v, ok := p.GetFloat(name)
		if !ok || (positive && v <= 0) {
			return nil, false
		}
		size, _ := p.GetFloat("text-size")
		return base + v/size, true
	}
}

func positiveValue(name string) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		v, ok := p.GetFloat(name)
		return v, ok && v > 0
	}
}

func boolValue(name string) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		return p.GetBool(name)
	}
}

func transformValue(p *mss.Properties) (interface{}, bool) {
	v, ok := p.GetString("text-transform")
	switch v {
	case "uppercase", "lowercase", "none":
		return v, ok
	}
	return nil, false
}

func justifyValue(p *mss.Properties) (interface{}, bool) {
	v, ok := p.GetString("text-justify-alignment")
	switch v {
	case "left", "center", "right", "auto":
		return v, ok
	}
	return nil, false
}

func placementValue(p *mss.Properties) (interface{}, bool) {
	v, ok := p.GetString("text-placement")
	if !ok {
		return nil, false
	}
	if v == "line" {
		return "line", true
	}
	return "point", true
}

func isLinePlacement(p *mss.Properties) bool {
	v, _ := p.GetString("text-placement")
	return v == "line"
}

// haloColorValue returns text-halo-fill, or Mapnik's default white where
// only text-halo-radius is set.
func haloColorValue(p *mss.Properties) (interface{}, bool) {
	if v, ok := colorValue("text-halo-fill")(p); ok {
		return v, true
	}
	if _, ok := p.GetFloat("text-halo-radius"); ok {
		return "#ffffff", true
	}
	return nil, false
}

// directions are Mapnik's simple placements with the signs of the
// displacement; "X" keeps text-dx and text-dy as they are.
var directions = map[string][2]float64{
	"N": {0, -1}, "S": {0, 1}, "E": {1, 0}, "W": {-1, 0},
	"NE": {1, -1}, "SE": {1, 1}, "NW": {-1, -1}, "SW": {-1, 1},
}

// placementDirections returns the directions of text-placements for
// text-placement-type simple, and whether the list also has sizes.
func placementDirections(p *mss.Properties) ([]string, bool) {
	if t, _ := p.GetString("text-placement-type"); t != "simple" {
		return nil, false
	}
	list, _ := p.GetString("text-placements")
	var dirs []string
	sizes := false
	for _, d := range strings.Split(list, ",") {
		d = strings.TrimSpace(d)
		if _, ok := directions[d]; ok || d == "X" {
			dirs = append(dirs, d)
		} else if d != "" {
			sizes = true
		}
	}
	return dirs, sizes
}

// anchorValue returns the anchors and offsets (in ems) of a point label as
// Mapnik places it: each direction displaces the label by text-dx/text-dy
// with the direction's sign, and the alignment, unless set, follows the
// displacement, e.g. text right of the point for a positive dx.
func anchorValue(p *mss.Properties) (interface{}, bool) {
	if isLinePlacement(p) {
		return nil, false
	}
	_, okx := p.GetFloat("text-dx")
	_, oky := p.GetFloat("text-dy")
	_, okh := p.GetString("text-horizontal-alignment")
	_, okv := p.GetString("text-vertical-alignment")
	if dirs, _ := placementDirections(p); !okx && !oky && !okh && !okv && len(dirs) == 0 {
		return nil, false
	}
	return literal(anchorList(p)), true
}

// anchorList returns the anchor and offset pairs of anchorValue.
func anchorList(p *mss.Properties) []interface{} {
	dx, _ := p.GetFloat("text-dx")
	dy, _ := p.GetFloat("text-dy")
	halign, _ := p.GetString("text-horizontal-alignment")
	valign, _ := p.GetString("text-vertical-alignment")
	dirs, _ := placementDirections(p)
	if len(dirs) == 0 {
		dirs = []string{"X"}
	}
	size, _ := p.GetFloat("text-size")
	var result []interface{}
	for _, d := range dirs {
		x, y := dx, dy
		if sign, ok := directions[d]; ok {
			x, y = sign[0]*math.Abs(dx), sign[1]*math.Abs(dy)
		}
		result = append(result, anchorName(halign, valign, x, y), []float64{em(x, size), em(y, size)})
	}
	return result
}

// anchorName returns the MapLibre text-anchor, the side of the text at the
// point, for Mapnik's alignment of the text relative to the point.
func anchorName(halign, valign string, x, y float64) string {
	if halign == "" || halign == "auto" || halign == "adjust" {
		switch {
		case x > 0:
			halign = "right"
		case x < 0:
			halign = "left"
		}
	}
	if valign == "" || valign == "auto" {
		switch {
		case y > 0:
			valign = "bottom"
		case y < 0:
			valign = "top"
		}
	}
	var v, h string
	switch valign {
	case "top":
		v = "bottom"
	case "bottom":
		v = "top"
	}
	switch halign {
	case "right":
		h = "left"
	case "left":
		h = "right"
	}
	switch {
	case v != "" && h != "":
		return v + "-" + h
	case v != "":
		return v
	case h != "":
		return h
	}
	return "center"
}

func em(px, size float64) float64 {
	v := px / size
	if v == 0 {
		return 0 // no -0
	}
	return v
}

func lineOffsetValue(p *mss.Properties) (interface{}, bool) {
	if !isLinePlacement(p) {
		return nil, false
	}
	dx, okx := p.GetFloat("text-dx")
	dy, oky := p.GetFloat("text-dy")
	if !okx && !oky {
		return nil, false
	}
	size, _ := p.GetFloat("text-size")
	return literal([]float64{em(dx, size), em(dy, size)}), true
}

// IsLineLabel reports whether a layer places symbols along lines at any
// zoom level.
func IsLineLabel(l Layer) bool {
	return l.Type == "symbol" && strings.Contains(string(mustJSON(l.Layout["symbol-placement"])), `"line`)
}

// splitLineLabels splits a symbol layer with line placement into one layer
// per zoom band of text-size: MapLibre fits line labels with text-size at
// zoom 18, so a size that grows with the zoom would hide most labels at
// lower zoom levels. Other layers are returned as they are.
func splitLineLabels(l Layer) []Layer {
	if !IsLineLabel(l) {
		return []Layer{l}
	}
	step, ok := l.Layout["text-size"].([]interface{})
	if !ok || len(step) < 5 || step[0] != "step" || !equal(step[1], []interface{}{"zoom"}) {
		return []Layer{l}
	}
	type band struct {
		from  *int
		value interface{}
	}
	bands := []band{{l.MinZoom, step[2]}}
	for i := 3; i+1 < len(step); i += 2 {
		var z int
		switch v := step[i].(type) {
		case int:
			z = v
		case float64:
			z = int(v)
		default:
			return []Layer{l}
		}
		bands = append(bands, band{intPtr(z), step[i+1]})
	}
	var result []Layer
	for i, b := range bands {
		part := l
		part.MinZoom = b.from
		if i+1 < len(bands) {
			part.MaxZoom = bands[i+1].from
		}
		from := 0
		if b.from != nil {
			from = *b.from
		}
		part.ID = fmt.Sprintf("%s#zoom=%d", l.ID, from)
		part.Layout = map[string]interface{}{}
		for k, v := range l.Layout {
			part.Layout[k] = v
		}
		part.Layout["text-size"] = b.value
		result = append(result, part)
	}
	return result
}
