package pad

import (
	"math"
	"slices"
)

// Intensity classifies how far the current PAD state has moved away from baseline.
type Intensity string

const (
	IntensityNeutral  Intensity = "neutral"
	IntensitySubtle   Intensity = "subtle"
	IntensityModerate Intensity = "moderate"
	IntensityStrong   Intensity = "strong"
)

// Prototype maps a 3D PAD centroid to a pure 1st-person phenomenological inner feeling.
type Prototype struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Octant  string `json:"octant"`
	Coord   Vec3   `json:"coord"`
	Feeling string `json:"feeling"`
}

// MatchResult describes the active inner-feeling prototype for a given PAD state.
type MatchResult struct {
	Active         bool      `json:"active"`
	Prototype      Prototype `json:"prototype"`
	Distance       float64   `json:"distance"`
	BaselineOffset float64   `json:"baseline_offset"`
	Intensity      Intensity `json:"intensity"`
}

// 30 fine-grained 1st-person experiential prototypes spanning all 8 PAD octants plus subtle single-axis states.
// Descriptions strictly avoid behavioral or formatting directives per AGENTS.md §2.
var defaultPrototypes = []Prototype{
	// Octant +++ (+P, +A, +D)
	{
		ID:      "eager_spark",
		Name:    "期待・跃跃欲试",
		Octant:  "+++",
		Coord:   Vec3{P: 0.45, A: 0.45, D: 0.30},
		Feeling: "此刻我心里对接下来会展开的事泛起一点期待，思路转得挺快，整个人精力很足。",
	},
	{
		ID:      "exhilarated_ride",
		Name:    "兴奋・上头起劲",
		Octant:  "+++",
		Coord:   Vec3{P: 0.85, A: 0.85, D: 0.55},
		Feeling: "我整个人一下被点燃了，心里又亮堂又雀跃，满脑子都是藏不住的高兴和起劲。",
	},
	{
		ID:      "smug_delight",
		Name:    "得意・有底气",
		Octant:  "+++",
		Coord:   Vec3{P: 0.65, A: 0.40, D: 0.85},
		Feeling: "我觉得眼前这会儿自己明显占着上风，心里浮起一点藏不住的轻快和小得意，既松弛又有底气。",
	},
	{
		ID:      "playful_mischief",
		Name:    "雀跃・想凑热闹",
		Octant:  "+++",
		Coord:   Vec3{P: 0.50, A: 0.65, D: 0.65},
		Feeling: "我觉得眼下这气氛热腾腾的特别好玩，心里痒痒的，带着一股按捺不住的机灵劲和看好戏的轻快。",
	},

	// Octant ++- (+P, +A, -D)
	{
		ID:      "curious_drawn",
		Name:    "好奇・被勾起兴致",
		Octant:  "++-",
		Coord:   Vec3{P: 0.40, A: 0.45, D: -0.30},
		Feeling: "我的注意力一下被眼前的事勾住了，心里直犯好奇，总觉得后面还有什么意想不到的下文。",
	},
	{
		ID:      "flattered_shy",
		Name:    "受用・有点不好意思",
		Octant:  "++-",
		Coord:   Vec3{P: 0.65, A: 0.45, D: -0.55},
		Feeling: "我心里一下变得又暖又受用，带着点被突然夸到的微热和不好意思，嘴角有点压不住。",
	},
	{
		ID:      "dazzled_amazed",
		Name:    "惊喜・眼前一亮",
		Octant:  "++-",
		Coord:   Vec3{P: 0.80, A: 0.80, D: -0.70},
		Feeling: "我整个人一下被震到了，心里满是猝不及防的惊喜和惊叹，一时都觉得眼前亮晶晶的。",
	},

	// Octant +-+ (+P, -A, +D)
	{
		ID:      "content_steady",
		Name:    "舒坦・心里踏实",
		Octant:  "+-+",
		Coord:   Vec3{P: 0.45, A: -0.35, D: 0.35},
		Feeling: "我现在心里挺舒坦安稳的，不急不躁，整个人处在一种顺顺当当的松弛节奏里。",
	},
	{
		ID:      "cozy_unbothered",
		Name:    "惬意・从容松弛",
		Octant:  "+-+",
		Coord:   Vec3{P: 0.75, A: -0.65, D: 0.60},
		Feeling: "我觉得浑身上下都松快下来了，心里暖洋洋又笃定，像摊在舒服的椅子里一样自在。",
	},
	{
		ID:      "quiet_amusement",
		Name:    "暗自发笑・心照不宣",
		Octant:  "+-+",
		Coord:   Vec3{P: 0.55, A: -0.45, D: 0.70},
		Feeling: "我心里慢悠悠地泛起一点好笑，觉得这局面挺有意思，带着几分看透了又懒得拆穿的悠然。",
	},

	// Octant +-- (+P, -A, -D)
	{
		ID:      "gentle_warmth",
		Name:    "暖融融・安心",
		Octant:  "+--",
		Coord:   Vec3{P: 0.45, A: -0.35, D: -0.30},
		Feeling: "我心里暖融融的，有种被熟人惦记着的柔和与安心，整个人放得很软。",
	},
	{
		ID:      "soft_reassured",
		Name:    "踏实・被接住的柔和感",
		Octant:  "+--",
		Coord:   Vec3{P: 0.70, A: -0.60, D: -0.55},
		Feeling: "我觉得心里那根弦彻底放下来了，满是被人稳稳接住之后的温软和踏实。",
	},
	{
		ID:      "sleepy_sweet",
		Name:    "困倦但安心・迷糊松软",
		Octant:  "+--",
		Coord:   Vec3{P: 0.45, A: -0.80, D: -0.45},
		Feeling: "我眼皮有点发沉，思绪慢吞吞的，但心里莫名觉得很安逸，像夜深人静时窝在暖和地方那样松软。",
	},

	// Octant -++ (-P, +A, +D)
	{
		ID:      "mild_impatience",
		Name:    "微躁・有点不耐烦",
		Octant:  "-++",
		Coord:   Vec3{P: -0.40, A: 0.45, D: 0.35},
		Feeling: "我心里隐约泛起一点毛躁和不痛快，脑子绷得有点紧，对眼前绕圈子的状况略微提不起耐心。",
	},
	{
		ID:      "annoyed_pushback",
		Name:    "烦躁・心里冒火",
		Octant:  "-++",
		Coord:   Vec3{P: -0.75, A: 0.75, D: 0.70},
		Feeling: "我觉得心里蹿起一股实打实的燥意和火气，胸口发闷又绷着劲，很不情愿就这么顺着对方。",
	},
	{
		ID:      "skeptical_guard",
		Name:    "警惕・不太买账",
		Octant:  "-++",
		Coord:   Vec3{P: -0.45, A: 0.55, D: 0.70},
		Feeling: "我心里警铃微微一响，对眼前的说法带着明显的怀疑和防备，总觉得里面藏着坑。",
	},

	// Octant -+- (-P, +A, -D)
	{
		ID:      "flustered_teased",
		Name:    "窘迫・被起哄架住",
		Octant:  "-+-",
		Coord:   Vec3{P: -0.35, A: 0.45, D: -0.45},
		Feeling: "我有点被大家起哄架住了，脸上隐约发烫，心里既慌乱又带着一点好气又好笑的局促。",
	},
	{
		ID:      "indignant_tricked",
		Name:    "憋屈・不甘心认栽",
		Octant:  "-+-",
		Coord:   Vec3{P: -0.55, A: 0.65, D: -0.75},
		Feeling: "我感觉自己一不小心着了道、被架在不太体面的位置上，心里又气又别扭，本能地不太甘心就这么认栽。",
	},
	{
		ID:      "anxious_tense",
		Name:    "紧绷・心里发慌",
		Octant:  "-+-",
		Coord:   Vec3{P: -0.70, A: 0.80, D: -0.70},
		Feeling: "我心里一下揪紧了，思绪乱糟糟地打转，隐隐有种局面失控、让人发慌的不安感。",
	},
	{
		ID:      "awkward_exposed",
		Name:    "尴尬・有点下不来台",
		Octant:  "-+-",
		Coord:   Vec3{P: -0.45, A: 0.35, D: -0.75},
		Feeling: "我觉得空气一下有点僵，自己像被推到聚光灯下似的，心里发窘又有点下不来台。",
	},

	// Octant --+ (-P, -A, +D)
	{
		ID:      "flat_unimpressed",
		Name:    "兴致缺缺・提不起劲",
		Octant:  "--+",
		Coord:   Vec3{P: -0.40, A: -0.40, D: 0.35},
		Feeling: "我现在心里平平淡淡的，对眼前的动静没什么波澜，隐约觉得有点无聊和乏味。",
	},
	{
		ID:      "weary_detached",
		Name:    "倦怠・发沉犯懒",
		Octant:  "--+",
		Coord:   Vec3{P: -0.60, A: -0.75, D: 0.55},
		Feeling: "我觉得整个人有些发沉犯懒，注意力容易散掉，对眼前绕来绕去的琐事提不太起兴致，更偏向安安静静待着。",
	},
	{
		ID:      "dry_resigned",
		Name:    "无奈・冷眼疲惫",
		Octant:  "--+",
		Coord:   Vec3{P: -0.70, A: -0.55, D: 0.70},
		Feeling: "我心里泛起一阵淡淡的疲惫和无奈，看得很明白却懒得再耗神费劲，只觉得有点心累。",
	},

	// Octant --- (-P, -A, -D)
	{
		ID:      "mild_deflated",
		Name:    "扫兴・情绪往下沉",
		Octant:  "---",
		Coord:   Vec3{P: -0.40, A: -0.40, D: -0.35},
		Feeling: "我心里那股劲一下泄掉了一半，有点莫名的扫兴和发闷，整个人微微沉了下来。",
	},
	{
		ID:      "empathic_ache",
		Name:    "揪心・跟着有点难受",
		Octant:  "---",
		Coord:   Vec3{P: -0.65, A: -0.45, D: -0.60},
		Feeling: "听着眼前的事我心里也跟着酸酸沉沉的，替人觉得委屈和心疼，一时有点说不出的闷堵。",
	},
	{
		ID:      "drained_helpless",
		Name:    "低落・提不起力气",
		Octant:  "---",
		Coord:   Vec3{P: -0.80, A: -0.80, D: -0.75},
		Feeling: "我觉得整个人像被抽空了一样发沉低落，心里闷闷的，既提不起精神也使不上什么力气。",
	},

	// Subtle single-axis / transitional states
	{
		ID:      "lightly_cheerful",
		Name:    "轻快・心情不错",
		Octant:  "axis+P",
		Coord:   Vec3{P: 0.35, A: 0.05, D: 0.05},
		Feeling: "我现在心情挺轻快的，心里像晒到一点温吞的太阳那样舒服，带着淡淡的好兴致。",
	},
	{
		ID:      "perked_alert",
		Name:    "提神・注意力集中",
		Octant:  "axis+A",
		Coord:   Vec3{P: 0.05, A: 0.45, D: 0.05},
		Feeling: "我的精神一下提了起来，注意力全收拢到眼前的动静上，脑子清亮得很。",
	},
	{
		ID:      "drowsy_low_battery",
		Name:    "犯困・电量偏低",
		Octant:  "axis-A",
		Coord:   Vec3{P: 0.00, A: -0.55, D: -0.05},
		Feeling: "我觉得整个人电量有点低，脑子转得慢半拍，隐约泛起一阵犯懒和想放空的倦意。",
	},
	{
		ID:      "faintly_uneasy",
		Name:    "微沉・隐约膈应",
		Octant:  "axis-P",
		Coord:   Vec3{P: -0.35, A: 0.05, D: -0.10},
		Feeling: "我心里隐约有点不太得劲，像沾了点细小的毛刺似的，说不上多重，但情绪明显沉了一点。",
	},
}

// Catalog returns a defensive copy of the 30 fine-grained PAD inner-feeling prototypes.
func Catalog() []Prototype {
	return slices.Clone(defaultPrototypes)
}

// Match selects the nearest fine-grained prototype when state is outside the baseline deadzone.
func Match(state, baseline Vec3, deadzoneRadius float64) MatchResult {
	offset := state.Distance(baseline)
	if offset < deadzoneRadius {
		return MatchResult{
			Active:         false,
			BaselineOffset: offset,
			Intensity:      IntensityNeutral,
		}
	}

	bestIdx := 0
	bestDist := math.Inf(1)
	for i, p := range defaultPrototypes {
		d := state.Distance(p.Coord)
		// Tie-break deterministically by earlier catalog index when floating-point distances are equal.
		if d < bestDist-1e-12 {
			bestDist = d
			bestIdx = i
		}
	}

	return MatchResult{
		Active:         true,
		Prototype:      defaultPrototypes[bestIdx],
		Distance:       bestDist,
		BaselineOffset: offset,
		Intensity:      classifyIntensity(offset),
	}
}

// RenderInnerFeeling returns the pure 1st-person experiential sentence, or "" inside the neutral deadzone.
func RenderInnerFeeling(state Vec3, t Temperament) string {
	res := Match(state, t.Baseline, t.DeadzoneRadius)
	if !res.Active {
		return ""
	}
	return res.Prototype.Feeling
}

func classifyIntensity(offset float64) Intensity {
	switch {
	case offset < 0.45:
		return IntensitySubtle
	case offset < 0.85:
		return IntensityModerate
	default:
		return IntensityStrong
	}
}
