package maplibre

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/omniscale/magnacarto/mss"
)

// Icons: point-, shield- and marker- images are sprite images named like
// pattern files (see imageValue). Mapnik scales them with *-transform,
// MapLibre with icon-size relative to the sprite image, which may be
// rounded to whole pixels (Options.ImageSize).

var pointConverter = symConverter{
	layerType: "symbol",
	draws: func(p *mss.Properties) bool {
		f, ok := p.GetString("point-file")
		return ok && f != ""
	},
	props: map[string]propConv{
		"point-file": {target: "icon-image", layout: true, mapValue: iconImageValue("point-file")},
		"point-transform": {target: "icon-size", layout: true, always: true, def: 1.0,
			mapValue: iconSizeValue("point-file", "point-transform")},
		"point-transform (offset)": {target: "icon-offset", layout: true, def: literal([]float64{0, 0}),
			mapValue: iconOffsetValue("point-file", "point-transform")},
		"point-allow-overlap":    {target: "icon-allow-overlap", layout: true, zoomOnly: true, def: false, value: boolValue("point-allow-overlap")},
		"point-ignore-placement": {target: "icon-ignore-placement", layout: true, zoomOnly: true, def: false, value: boolValue("point-ignore-placement")},
		"point-opacity":          {target: "icon-opacity", value: floatValue("point-opacity", 1), def: 1.0},
		// Mapnik keeps no space around points, MapLibre 2 px by default.
		"point-margin": {target: "icon-padding", layout: true, zoomOnly: true, always: true, def: 0.0, value: noValue},
	},
	// The Projektfunktion sends one label point per feature
	// (Options.Rewrite), wherever Mapnik would place it.
	handled: []string{"point-placement"},
	approximate: map[string]func(m *Map, p *mss.Properties) (string, bool){
		"point-transform": transformApproximated("point-transform"),
	},
	omitted: map[string]string{
		"point-comp-op": "MapLibre has no compositing operations",
	},
	constLayout: map[string]interface{}{"symbol-z-order": "source"},
}

// iconImageValue returns the sprite image of an icon (Options.IconImage).
func iconImageValue(file string) func(m *Map, p *mss.Properties) (interface{}, bool) {
	return func(m *Map, p *mss.Properties) (interface{}, bool) {
		image, ok := imageValue(file)(p)
		if !ok || m.opts.IconImage == nil {
			return image, ok
		}
		return m.opts.IconImage(image.(string)), true
	}
}

func noValue(p *mss.Properties) (interface{}, bool) { return nil, false }

var transformRe = regexp.MustCompile(`\s*([a-z]+)\(([^)]*)\)\s*,?`)

// iconTransform is a uniform scale and a translation in pixels on screen.
type iconTransform struct {
	scale, dx, dy float64
}

// transformOf returns the transform of an icon; ok is false if the
// transform has functions other than a uniform scale and translate, which
// are ignored.
func transformOf(p *mss.Properties, name string) (t iconTransform, set, ok bool) {
	t = iconTransform{scale: 1}
	v, set := p.GetString(name)
	if !set || strings.TrimSpace(v) == "" {
		return t, false, true
	}
	ok = true
	rest := v
	for strings.TrimSpace(rest) != "" {
		m := transformRe.FindStringSubmatchIndex(rest)
		if m == nil || m[0] != 0 {
			return t, true, false
		}
		fn, args := rest[m[2]:m[3]], strings.FieldsFunc(rest[m[4]:m[5]], func(r rune) bool { return r == ',' || r == ' ' })
		rest = rest[m[1]:]
		var nums []float64
		for _, a := range args {
			f, err := strconv.ParseFloat(a, 64)
			if err != nil {
				return t, true, false
			}
			nums = append(nums, f)
		}
		// The list applies from right to left: each function to the
		// result of the following ones, so its translation is scaled by
		// the scales before it.
		switch {
		case fn == "scale" && (len(nums) == 1 || len(nums) == 2 && nums[0] == nums[1]):
			t.scale *= nums[0]
		case fn == "translate" && (len(nums) == 1 || len(nums) == 2):
			ty := 0.0
			if len(nums) == 2 {
				ty = nums[1]
			}
			t.dx += t.scale * nums[0]
			t.dy += t.scale * ty
		default:
			ok = false
		}
	}
	return t, true, ok
}

func transformApproximated(name string) func(m *Map, p *mss.Properties) (string, bool) {
	return func(_ *Map, p *mss.Properties) (string, bool) {
		if _, set, ok := transformOf(p, name); set && !ok {
			return "only uniform scale() and translate() are converted (icon-size, icon-offset), other transforms are ignored", true
		}
		return "", false
	}
}

// imageRatio returns the native width of an image over the width of its
// sprite image.
func (m *Map) imageRatio(p *mss.Properties, file string) float64 {
	image, ok := imageValue(file)(p)
	if !ok || m.opts.ImageSize == nil {
		return 1
	}
	native, sprite, ok := m.opts.ImageSize(image.(string))
	if !ok || native <= 0 || sprite <= 0 {
		return 1
	}
	return native / sprite
}

// iconSizeValue returns the scale of the transform as icon-size, corrected
// for the size of the sprite image.
func iconSizeValue(file, transform string) func(m *Map, p *mss.Properties) (interface{}, bool) {
	return func(m *Map, p *mss.Properties) (interface{}, bool) {
		t, _, _ := transformOf(p, transform)
		return roundSize(t.scale * m.imageRatio(p, file)), true
	}
}

// iconOffsetValue returns the translation of the transform as icon-offset,
// which MapLibre multiplies by icon-size.
func iconOffsetValue(file, transform string) func(m *Map, p *mss.Properties) (interface{}, bool) {
	return func(m *Map, p *mss.Properties) (interface{}, bool) {
		t, _, _ := transformOf(p, transform)
		if t.dx == 0 && t.dy == 0 {
			return nil, false
		}
		size := t.scale * m.imageRatio(p, file)
		if size == 0 {
			return nil, false // nothing is drawn
		}
		return literal([]float64{roundSize(t.dx / size), roundSize(t.dy / size)}), true
	}
}

// roundSize keeps icon sizes short in the style.
func roundSize(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 4, 64), 64)
	return r
}

// Shields are an image with a text: one symbol layer with icon and text,
// which MapLibre places only together, like Mapnik. The text properties are
// converted as text-* (shieldText). The image stays at the point and the
// text takes the placements and shield-text-dx/dy, as with
// shield-unlock-image; without it Mapnik also centres the image at the
// point as long as the whole shield is not displaced (shield-dx/dy). Mapnik
// keeps shields upright, also along lines.

// shieldNames are the shield properties of text properties that are not
// named shield-<name without text->.
var shieldNames = map[string]string{
	"text-dx":        "shield-text-dx",
	"text-dy":        "shield-text-dy",
	"text-transform": "shield-text-transform",
	"text-opacity":   "shield-text-opacity",
}

func shieldName(text string) string {
	if n, ok := shieldNames[text]; ok {
		return n
	}
	return "shield-" + strings.TrimPrefix(text, "text-")
}

// textNames maps the shield properties of all known text properties back.
var textNames = func() map[string]string {
	names := map[string]string{}
	add := func(text string) { names[shieldName(text)] = text }
	for k := range textConverter.props {
		add(k)
	}
	for k := range textConverter.omitted {
		add(k)
	}
	for k := range textConverter.approximate {
		add(k)
	}
	for _, k := range textConverter.handled {
		add(k)
	}
	add(textConverter.label.repeatDistance)
	return names
}()

// shieldText returns the text properties of a shield as text-*.
func shieldText(p *mss.Properties) *mss.Properties {
	return p.Renamed(func(name string) (string, bool) {
		t, ok := textNames[name]
		return t, ok
	})
}

var shieldConverter = newShieldConverter()

func newShieldConverter() symConverter {
	viaText := func(f func(p *mss.Properties) (interface{}, bool)) func(p *mss.Properties) (interface{}, bool) {
		return func(p *mss.Properties) (interface{}, bool) { return f(shieldText(p)) }
	}
	c := symConverter{
		layerType: "symbol",
		draws: func(p *mss.Properties) bool {
			f, ok := p.GetString("shield-file")
			return ok && f != ""
		},
		props: map[string]propConv{
			"shield-file": {target: "icon-image", layout: true, mapValue: iconImageValue("shield-file")},
			"shield-transform": {target: "icon-size", layout: true, always: true, def: 1.0,
				mapValue: iconSizeValue("shield-file", "shield-transform")},
			"shield-transform (offset)": {target: "icon-offset", layout: true, def: literal([]float64{0, 0}),
				mapValue: iconOffsetValue("shield-file", "shield-transform")},
			"shield-opacity": {target: "icon-opacity", value: floatValue("shield-opacity", 1), def: 1.0},
		},
		omitted:     map[string]string{},
		approximate: map[string]func(m *Map, p *mss.Properties) (string, bool){"shield-transform": transformApproximated("shield-transform")},
		handled:     []string{"shield-unlock-image"},
		constLayout: map[string]interface{}{
			"symbol-z-order":          "source",
			"icon-padding":            0.0,
			"icon-rotation-alignment": "viewport",
			"text-rotation-alignment": "viewport",
		},
		label: &labelProps{view: shieldText, placement: map[string]bool{}, repeatDistance: "shield-repeat-distance"},
	}
	for k, pc := range textConverter.props {
		if pc.value != nil {
			pc.value = viaText(pc.value)
		}
		if mv := pc.mapValue; mv != nil {
			pc.mapValue = func(m *Map, p *mss.Properties) (interface{}, bool) { return mv(m, shieldText(p)) }
		}
		c.props[shieldName(k)] = pc
	}
	// Along a line Mapnik places a shield no closer to one with the same
	// text than the repeat distance; MapLibre at least keeps it along the
	// same line.
	spacing := c.props["shield-spacing"]
	spacing.value = lineShieldSpacing
	c.props["shield-spacing"] = spacing
	c.approximate["shield-repeat-distance"] = func(_ *Map, p *mss.Properties) (string, bool) {
		if d, ok := p.GetFloat("shield-repeat-distance"); ok && d > 0 && isLinePlacement(shieldText(p)) {
			return "along lines as the minimum symbol-spacing; MapLibre has no minimum distance between labels of different features", true
		}
		return "", false
	}
	// Mapnik's default size; shields draw without one.
	size := c.props["shield-size"]
	size.def = defaultTextSize
	size.always = true
	c.props["shield-size"] = size
	// The image is drawn also without a name.
	field := c.props["shield-name"]
	field.def = ""
	c.props["shield-name"] = field
	// Both icon and text keep clear of other symbols unless allowed.
	overlap := c.props["shield-allow-overlap"]
	overlap.target = "icon-allow-overlap"
	c.props["shield-allow-overlap (icon)"] = overlap
	for k, v := range textConverter.omitted {
		c.omitted[shieldName(k)] = v
	}
	for k, f := range textConverter.approximate {
		f := f
		c.approximate[shieldName(k)] = func(m *Map, p *mss.Properties) (string, bool) { return f(m, shieldText(p)) }
	}
	for _, k := range textConverter.handled {
		c.handled = append(c.handled, shieldName(k))
	}
	for k := range textConverter.label.placement {
		c.label.placement[shieldName(k)] = true
	}
	return c
}

// Markers are drawn as icons, along lines with marker-placement line, where
// MapLibre turns them with the line like Mapnik. Mapnik scales the image to
// marker-width, which needs the width of the sprite image
// (Options.ImageSize).
var markerConverter = symConverter{
	layerType: "symbol",
	draws: func(p *mss.Properties) bool {
		f, ok := p.GetString("marker-file")
		return ok && f != ""
	},
	props: map[string]propConv{
		"marker-file":  {target: "icon-image", layout: true, mapValue: iconImageValue("marker-file")},
		"marker-width": {target: "icon-size", layout: true, always: true, def: 1.0, mapValue: markerSizeValue},
		"marker-placement": {target: "symbol-placement", layout: true, zoomOnly: true, def: "point", value: func(p *mss.Properties) (interface{}, bool) {
			v, ok := p.GetString("marker-placement")
			if v == "line" {
				return "line", ok
			}
			return "point", ok
		}},
		// Mapnik's default spacing is 100 px, MapLibre's 250.
		"marker-spacing":          {target: "symbol-spacing", layout: true, zoomOnly: true, always: true, def: 100.0, value: positiveValue("marker-spacing")},
		"marker-allow-overlap":    {target: "icon-allow-overlap", layout: true, zoomOnly: true, def: false, value: boolValue("marker-allow-overlap")},
		"marker-ignore-placement": {target: "icon-ignore-placement", layout: true, zoomOnly: true, def: false, value: boolValue("marker-ignore-placement")},
		"marker-opacity":          {target: "icon-opacity", value: floatValue("marker-opacity", 1), def: 1.0},
		// Mapnik keeps no space around markers, MapLibre 2 px by default.
		"marker-margin": {target: "icon-padding", layout: true, zoomOnly: true, always: true, def: 0.0, value: noValue},
	},
	handled: []string{"marker-transform"},
	approximate: map[string]func(m *Map, p *mss.Properties) (string, bool){
		"marker-transform": func(m *Map, p *mss.Properties) (string, bool) {
			if t, set, ok := transformOf(p, "marker-transform"); set && (!ok || t.dx != 0 || t.dy != 0) {
				return "only a uniform scale() is converted for markers (icon-size), other transforms are ignored", true
			}
			return "", false
		},
		"marker-width": func(m *Map, p *mss.Properties) (string, bool) {
			if _, ok := m.spriteWidth(p, "marker-file"); !ok {
				return "the width of the sprite image is unknown (Options.ImageSize), the marker keeps it", true
			}
			return "", false
		},
	},
	omitted: map[string]string{
		"marker-max-error": "MapLibre places icons along lines by symbol-spacing only",
		"marker-comp-op":   "MapLibre has no compositing operations",
	},
	constLayout: map[string]interface{}{"symbol-z-order": "source"},
}

// spriteWidth returns the width of the sprite image of a file.
func (m *Map) spriteWidth(p *mss.Properties, file string) (float64, bool) {
	image, ok := imageValue(file)(p)
	if !ok || m.opts.ImageSize == nil {
		return 0, false
	}
	_, sprite, ok := m.opts.ImageSize(image.(string))
	return sprite, ok && sprite > 0
}

// markerSizeValue returns marker-width over the sprite width as icon-size,
// times the scale of marker-transform.
func markerSizeValue(m *Map, p *mss.Properties) (interface{}, bool) {
	t, _, _ := transformOf(p, "marker-transform")
	scale := t.scale
	if w, ok := p.GetFloat("marker-width"); ok {
		if sprite, ok := m.spriteWidth(p, "marker-file"); ok {
			return roundSize(scale * w / sprite), true
		}
	}
	return roundSize(scale * m.imageRatio(p, "marker-file")), true
}

// lineShieldSpacing returns the larger of shield-spacing and, along lines,
// shield-repeat-distance.
func lineShieldSpacing(p *mss.Properties) (interface{}, bool) {
	spacing, ok := p.GetFloat("shield-spacing")
	if !ok || spacing <= 0 {
		spacing, ok = 0, false
	}
	if d, set := p.GetFloat("shield-repeat-distance"); set && d > spacing && isLinePlacement(shieldText(p)) {
		spacing, ok = d, true
	}
	return spacing, ok
}
