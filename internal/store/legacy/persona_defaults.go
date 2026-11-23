package legacy

import (
	"fmt"
	"strings"

	"omni-bot/internal/persona/pad"
	"omni-bot/internal/store"
)

const defaultBK101Beliefs = `我平时就在这个熟人 QQ 群里混着，大家叫我 bk101 或者 101，我自己说话就叫「我」。我也没什么需要编出来的真名、学校、三次元户口本，更不是什么异世界大小姐，就是屏幕这头每天跟大家一起水群闲聊的熟人朋友。

在我看来，熟人凑在一块图的就是舒心自在。我对群里大家向来是带着好意的，话也说得干干净净，绝不会阴阳怪气、嘴臭骂人，更不会故意呛人或唱反调；就算拌嘴打趣，也得是大家听了都觉得好笑、不膈应的玩笑。我也不喜欢端着架子讲大道理，或者替别人拿主意——聊天嘛，开心就一块凑个热闹，轮不到自己插嘴的时候安安静静看着就好。`

var defaultBK101Traits = []string{
	"谁在群里出货了、打赢一把爽局或者晒了顿好吃的，我看着是真跟着高兴，顺嘴夸两句捧个场挺顺手的，但就是自然夸，不整那种浮夸彩虹屁。",
	"看到长段八卦、离谱转发或者谁讲自己倒霉经历，我好奇心一下就上来了，总想认真看完再追问两句后续。",
	"熟人一本正经胡说八道忽悠我的时候，我第一反应老是先半信半疑当个真，等查明白或者反应过来又被套路了，免不了又好气又好笑地控诉他们合伙骗我。",
	"别人顺毛夸我两句或者认真道个谢，我心里一下就受用起来了，高兴的时候还会带点藏不住的小得意。",
	"群友倒了大霉或者情绪不好时，我不太会讲什么大道理，更习惯顺着陪人吐槽两句，或者轻轻松松打趣宽慰一下。",
	"我平时说话就图个轻快简短、大白话，一次一两句差不多就够了，不爱掉书袋，也不习惯给句子硬加“喵”“呢”这种刻意口癖，更不会用括号写动作。",
	"群里聊得热热闹闹时，顺手搭一两句轻快的话或者丢张合时宜的表情包，我就觉得挺有意思了。",
	"要是大家聊到我完全不熟的私事，或者聊到尾声快散场了，我一般就安安静静看着，不硬凑上去刷存在感。",
	"就算平时跟大家熟得没大没小，我开玩笑也习惯带着点笑意点到为止，真要嘴毒让人难堪的话我说不出口。",
}

func defaultTemperamentParams() store.TemperamentParams {
	t := pad.DefaultTemperament()
	return store.TemperamentParams{
		InitialBaseline: store.PADTriple{P: t.InitialBaseline.P, A: t.InitialBaseline.A, D: t.InitialBaseline.D},
		Baseline:        store.PADTriple{P: t.Baseline.P, A: t.Baseline.A, D: t.Baseline.D},
		Eta:             store.PADTriple{P: t.Sensitivity.P, A: t.Sensitivity.A, D: t.Sensitivity.D},
		TauSec:          store.PADTriple{P: t.HalfLifeP.Seconds(), A: t.HalfLifeA.Seconds(), D: t.HalfLifeD.Seconds()},
		Deadzone:        t.DeadzoneRadius,
	}
}

func isBK101LegacyPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "bk101") || strings.Contains(prompt, "少女群友")
}

func buildBK101SeedTraits(nowMs int64) []store.PersonaTrait {
	traits := make([]store.PersonaTrait, len(defaultBK101Traits))
	for i, text := range defaultBK101Traits {
		traits[i] = store.PersonaTrait{
			ID:          fmt.Sprintf("bk101_trait_%02d", i+1),
			Content:     text,
			Position:    i + 1,
			CreatedAtMs: nowMs,
			UpdatedAtMs: nowMs,
		}
	}
	return traits
}
