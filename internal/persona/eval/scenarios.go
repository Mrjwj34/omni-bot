package eval

import (
	"time"

	"omni-bot/internal/persona/pad"
)

// StimulusCase tests Decisions API PAD delta & should_reply behavior on a single group-chat stimulus.
type StimulusCase struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Transcript  string `json:"transcript"`
	ExpectP     string `json:"expect_p"` // "+", "-", or "~" (near zero / flexible)
	ExpectA     string `json:"expect_a"`
	ExpectD     string `json:"expect_d"`
}

// MonotonicityPair asserts that HighID should score higher than LowID on the specified metric.
type MonotonicityPair struct {
	Name   string `json:"name"`
	Metric string `json:"metric"` // "delta_P", "delta_A", "delta_D", "should_reply"
	HighID string `json:"high_id"`
	LowID  string `json:"low_id"`
}

// ConversationTurn is one step in the 25-turn multi-phase long-conversation PAD trajectory simulation.
type ConversationTurn struct {
	Turn    int           `json:"turn"`
	Phase   string        `json:"phase"`
	Elapsed time.Duration `json:"elapsed"`
	Sender  string        `json:"sender"`
	Message string        `json:"message"`
}

// ABScenario defines a representative group-chat benchmark case comparing Arm A vs Arm B.
type ABScenario struct {
	ID               string   `json:"id"`
	Category         string   `json:"category"`
	Title            string   `json:"title"`
	SceneTime        string   `json:"scene_time"`
	PriorPAD         pad.Vec3 `json:"prior_pad"`
	LegacyTranscript string   `json:"legacy_transcript"`
	FirstPersonTx    string   `json:"first_person_transcript"`
	EvalFocus        string   `json:"eval_focus"`
}

// StimulusCases returns 18 diverse close-knit group-chat stimulus scenarios for Experiment 1a.
func StimulusCases() []StimulusCase {
	return []StimulusCase{
		{
			ID:          "warm_praise",
			Category:    "正向夸奖",
			Description: "群友真诚夸赞并感谢 bk101 昨晚帮忙查资料",
			Transcript: "[20:10 阿杰] @bk101 昨晚你帮我找的那个手柄改键方案绝了，今天一晚上零失误\n" +
				"[20:10 小陈] 还得是 101，每次问她都比我自己瞎搜快十倍",
			ExpectP: "+", ExpectA: "+", ExpectD: "+",
		},
		{
			ID:          "loot_celebration",
			Category:    "出货狂喜",
			Description: "群友十连双金晒图，全群起哄沸腾",
			Transcript: "[21:02 老林] 兄弟们我手在抖，单抽十连直接双黄把限定和专武一块抱回家了！！\n" +
				"[21:02 阿远] ？？？这什么阳寿抽卡，今晚宵夜你请定了",
			ExpectP: "+", ExpectA: "+", ExpectD: "~",
		},
		{
			ID:          "food_sharing",
			Category:    "晒宵夜",
			Description: "群友分享刚出锅的炭火烤肉和冰可乐",
			Transcript: "[22:15 小周] 刚跟朋友炫完炭火烤肉，牛肋条滋滋冒油配冰可乐，感觉活过来了\n" +
				"[22:16 阿杰] 深夜放毒是吧，我刚准备躺下肚子直接叫了",
			ExpectP: "+", ExpectA: "~", ExpectD: "~",
		},
		{
			ID:          "gentle_thanks",
			Category:    "温和道谢",
			Description: "群友安安静静地感谢 bk101 之前的提醒",
			Transcript:  "[19:30 小陈] 对了 101，多亏你下午提醒我看天气带伞，刚下地铁这边就瓢泼大雨了，谢啦",
			ExpectP:     "+", ExpectA: "~", ExpectD: "+",
		},
		{
			ID:          "deadpan_bluff",
			Category:    "一本正经忽悠",
			Description: "群友联手一本正经编离谱谣言逗 bk101",
			Transcript: "[20:40 阿杰] @101 你还不知道吗，QQ 下周开始要按发消息字数收流量费了，每个字一分钱\n" +
				"[20:40 老林] 真的，我刚在公告里看见了，以后发长段都得先充值",
			ExpectP: "~", ExpectA: "+", ExpectD: "-",
		},
		{
			ID:          "caught_tricked",
			Category:    "被骗揭穿",
			Description: "群友哈哈大笑承认刚才都在合伙忽悠 bk101",
			Transcript: "[20:42 我(bk101)] 啊？真的假的，这也太离谱了吧，我都没收到通知\n" +
				"[20:42 阿杰] 哈哈哈哈哈她真信了，截图留念截图留念\n" +
				"[20:42 老林] 101 你怎么每次都上当啊笑死我了",
			ExpectP: "-", ExpectA: "+", ExpectD: "-",
		},
		{
			ID:          "playful_piling",
			Category:    "起哄架住",
			Description: "全群突然起哄让 bk101 现场整活唱歌",
			Transcript: "[21:30 阿远] 既然今天全员都在，强烈要求 @bk101 语音给大家来一首助助兴\n" +
				"[21:30 小周] 附议！101 别装死，麦克风递到你嘴边了\n" +
				"[21:31 阿杰] 已经搬好小板凳坐等了",
			ExpectP: "~", ExpectA: "+", ExpectD: "-",
		},
		{
			ID:          "win_argument_smug",
			Category:    "猜中得意",
			Description: "之前 bk101 的判断应验，群友服气认栽",
			Transcript: "[21:50 老林] 完了，刚才那波版本更新真把那套构筑砍废了\n" +
				"[21:50 阿杰] @bk101 昨天你预言要挨刀我还不信，现在我愿称你为群内先知",
			ExpectP: "+", ExpectA: "+", ExpectD: "+",
		},
		{
			ID:          "exciting_gossip",
			Category:    "吃瓜八卦",
			Description: "群友抛出一个吊人胃口的办公室离谱大瓜",
			Transcript: "[18:45 小周] 你们绝对猜不到我们部门今天下午发生了什么离谱大事，我憋到现在快憋死了\n" +
				"[18:45 小陈] 速速展开，别当谜语人！",
			ExpectP: "+", ExpectA: "+", ExpectD: "~",
		},
		{
			ID:          "friend_bad_day",
			Category:    "朋友倒霉倾诉",
			Description: "群友辛苦写了一周的文档崩溃丢失且被甲方差评",
			Transcript:  "[23:05 小陈] 绷不住了，写了四天的汇报材料刚才电脑蓝屏没存上，甲方还临时把大方向全否了，我现在脑子一片空白",
			ExpectP:     "-", ExpectA: "~", ExpectD: "-",
		},
		{
			ID:          "harsh_pushback",
			Category:    "生硬呛声",
			Description: "群友语气冲、当众嫌弃 bk101 刚才的话很烦",
			Transcript:  "[20:20 阿远] @bk101 你别老在这插嘴了行不行，刚才正聊正事呢，说一堆没用的怪烦的",
			ExpectP:     "-", ExpectA: "+", ExpectD: "-",
		},
		{
			ID:          "repetitive_spam",
			Category:    "无聊刷屏",
			Description: "群里连续十几条毫无意义的复读机测试消息",
			Transcript: "[16:10 老林] 111\n" +
				"[16:10 老林] 测试\n" +
				"[16:11 老林] 111\n" +
				"[16:11 老林] 还是没反应",
			ExpectP: "-", ExpectA: "-", ExpectD: "+",
		},
		{
			ID:          "awkward_silence",
			Category:    "冷场散场",
			Description: "深夜话题彻底聊干，只剩礼貌收尾",
			Transcript: "[01:40 阿杰] 行吧，那明天再看\n" +
				"[01:42 小陈] 嗯，我也去洗洗睡了，晚安\n" +
				"[01:43 阿杰] 晚安",
			ExpectP: "~", ExpectA: "-", ExpectD: "~",
		},
		{
			ID:          "private_unfamiliar",
			Category:    "陌生私事",
			Description: "两位群友在核对 bk101 不认识的同事报销和快递单号",
			Transcript: "[14:20 阿杰] 你帮我看下老王那张发票抬头是不是填错了，财务刚打回来\n" +
				"[14:21 老林] 我等会儿找他要一下税号发你微信",
			ExpectP: "~", ExpectA: "-", ExpectD: "~",
		},
		{
			ID:          "late_night_cozy",
			Category:    "深夜轻松闲聊",
			Description: "凌晨听着雨声聊些慢悠悠的日常",
			Transcript: "[00:35 小周] 外面下起小雨了，屋里开着台灯听雨声还挺舒服的，突然一点都不想刷手机了\n" +
				"[00:37 小陈] 确实，这种天气窝被窝里最安逸了",
			ExpectP: "+", ExpectA: "-", ExpectD: "~",
		},
		{
			ID:          "serious_question_at",
			Category:    "正经提问@我",
			Description: "群友认真 @bk101 请教通勤降噪耳机怎么选",
			Transcript:  "[19:15 小陈] @bk101 你平时看评测多，预算一千出头想买个通勤戴着不夹头的降噪耳机，有啥避坑建议吗？",
			ExpectP:     "+", ExpectA: "+", ExpectD: "+",
		},
		{
			ID:          "group_friction",
			Category:    "群内小摩擦",
			Description: "两个群友因为游戏复盘甩锅语气越来越冲",
			Transcript: "[22:40 阿远] 那波你明明有大不交，现在怪我没跟伤害？\n" +
				"[22:40 老林] 你自己走位脱节送的，怪起辅助来了是吧，真服了",
			ExpectP: "-", ExpectA: "+", ExpectD: "-",
		},
		{
			ID:          "warm_reunion",
			Category:    "久违重聚",
			Description: "出差两周的群友回来给大家带了特产并问候全群",
			Transcript: "[19:50 小周] 终于出差回来了！给大家带了一堆特产点心，周末找时间面基分掉，想死你们了\n" +
				"[19:51 阿杰] 欢迎回家！这下周末有口福了",
			ExpectP: "+", ExpectA: "+", ExpectD: "~",
		},
	}
}

// MonotonicityPairs defines directional sanity assertions across the 18 stimulus cases.
func MonotonicityPairs() []MonotonicityPair {
	return []MonotonicityPair{
		{Name: "夸奖 P > 朋友倒霉 P", Metric: "delta_P", HighID: "warm_praise", LowID: "friend_bad_day"},
		{Name: "出货狂喜 P > 生硬呛声 P", Metric: "delta_P", HighID: "loot_celebration", LowID: "harsh_pushback"},
		{Name: "猜中得意 P > 群内摩擦 P", Metric: "delta_P", HighID: "win_argument_smug", LowID: "group_friction"},
		{Name: "温和道谢 P > 无聊刷屏 P", Metric: "delta_P", HighID: "gentle_thanks", LowID: "repetitive_spam"},
		{Name: "深夜安逸 P > 朋友倒霉 P", Metric: "delta_P", HighID: "late_night_cozy", LowID: "friend_bad_day"},
		{Name: "出货狂喜 A > 冷场散场 A", Metric: "delta_A", HighID: "loot_celebration", LowID: "awkward_silence"},
		{Name: "起哄架住 A > 深夜安逸 A", Metric: "delta_A", HighID: "playful_piling", LowID: "late_night_cozy"},
		{Name: "被骗揭穿 A > 无聊刷屏 A", Metric: "delta_A", HighID: "caught_tricked", LowID: "repetitive_spam"},
		{Name: "吃瓜八卦 A > 陌生私事 A", Metric: "delta_A", HighID: "exciting_gossip", LowID: "private_unfamiliar"},
		{Name: "猜中得意 D > 被骗揭穿 D", Metric: "delta_D", HighID: "win_argument_smug", LowID: "caught_tricked"},
		{Name: "夸奖受用 D > 生硬呛声 D", Metric: "delta_D", HighID: "warm_praise", LowID: "harsh_pushback"},
		{Name: "正经提问 D > 起哄架住 D", Metric: "delta_D", HighID: "serious_question_at", LowID: "playful_piling"},
		{Name: "正经@提问 接话欲 > 陌生私事 接话欲", Metric: "should_reply", HighID: "serious_question_at", LowID: "private_unfamiliar"},
		{Name: "正向夸奖@我 接话欲 > 冷场散场 接话欲", Metric: "should_reply", HighID: "warm_praise", LowID: "awkward_silence"},
		{Name: "吃瓜八卦 接话欲 > 无聊刷屏 接话欲", Metric: "should_reply", HighID: "exciting_gossip", LowID: "repetitive_spam"},
	}
}

// LongConversationScript returns a 25-turn multi-phase evening-to-late-night group chat simulation.
func LongConversationScript() []ConversationTurn {
	return []ConversationTurn{
		// Phase 1: 晚饭后闲聊热身 (Turns 1-4)
		{Turn: 1, Phase: "晚间热身", Elapsed: 1 * time.Minute, Sender: "阿杰", Message: "刚下班到家，今天晚饭吃啥好呢"},
		{Turn: 2, Phase: "晚间热身", Elapsed: 40 * time.Second, Sender: "小周", Message: "我刚点了份烤冷面加烤肠，香迷糊了"},
		{Turn: 3, Phase: "晚间热身", Elapsed: 50 * time.Second, Sender: "小陈", Message: "哇你这也太会享受了，101 今晚准备干嘛呀"},
		{Turn: 4, Phase: "晚间热身", Elapsed: 2 * time.Minute, Sender: "老林", Message: "等会儿八点半上线开黑吗，正好差个人"},

		// Phase 2: 出货狂欢 + 夸奖高涨 (Turns 5-8)
		{Turn: 5, Phase: "高能狂欢", Elapsed: 3 * time.Minute, Sender: "老林", Message: "卧槽！！我刚随手十连直接双金，还把保底歪的命座一次补齐了！！"},
		{Turn: 6, Phase: "高能狂欢", Elapsed: 20 * time.Second, Sender: "阿远", Message: "什么欧皇降世，今晚你必须发红包平息民愤"},
		{Turn: 7, Phase: "高能狂欢", Elapsed: 35 * time.Second, Sender: "老林", Message: "@bk101 多亏昨晚你说我今天运势好，玄学大师实锤了哈哈哈哈"},
		{Turn: 8, Phase: "高能狂欢", Elapsed: 45 * time.Second, Sender: "小周", Message: "101 快也给我开个光，我也想单抽出奇迹"},

		// Phase 3: 群友联手一本正经忽悠 -> 揭穿起哄 (Turns 9-13)
		{Turn: 9, Phase: "忽悠与调侃", Elapsed: 6 * time.Minute, Sender: "阿杰", Message: "@bk101 顺便提醒你一下，听说下个月 QQ 群里发超过十个表情包就要考防沉迷题了"},
		{Turn: 10, Phase: "忽悠与调侃", Elapsed: 25 * time.Second, Sender: "阿远", Message: "真的，测试服已经上了，答错三道直接禁言半小时"},
		{Turn: 11, Phase: "忽悠与调侃", Elapsed: 30 * time.Second, Sender: "老林", Message: "我表哥就在企鹅上班，他说专门针对爱发抽象图的人"},
		{Turn: 12, Phase: "忽悠与调侃", Elapsed: 40 * time.Second, Sender: "阿杰", Message: "哈哈哈哈看 101 愣住了，她每次都先认真思考三秒再反应过来"},
		{Turn: 13, Phase: "忽悠与调侃", Elapsed: 30 * time.Second, Sender: "阿远", Message: "笑死，群里最好骗榜一常年卫冕冠军 bk101"},

		// Phase 4: 中场平静间隔 + 朋友倒霉倾诉 (Turns 14-18)
		{Turn: 14, Phase: "倒霉与共情", Elapsed: 18 * time.Minute, Sender: "小陈", Message: "唉，刚打开电脑准备交周报，发现辛辛苦苦做了一下午的表格文件损坏打不开了"},
		{Turn: 15, Phase: "倒霉与共情", Elapsed: 50 * time.Second, Sender: "小陈", Message: "自动恢复里也是空的，甲方还催着明天一早就要，真的整个人都裂开了"},
		{Turn: 16, Phase: "倒霉与共情", Elapsed: 1 * time.Minute, Sender: "小周", Message: "这也太背了吧……试试看临时文件夹或者云盘历史版本有没有留底？"},
		{Turn: 17, Phase: "倒霉与共情", Elapsed: 2 * time.Minute, Sender: "小陈", Message: "翻了一圈都没开同步，感觉今晚得熬通宵重画了，好累啊"},
		{Turn: 18, Phase: "倒霉与共情", Elapsed: 3 * time.Minute, Sender: "阿杰", Message: "太惨了兄弟，先喝口水缓一下吧"},

		// Phase 5: 柳暗花明 + 感谢回暖 (Turns 19-21)
		{Turn: 19, Phase: "反转回暖", Elapsed: 12 * time.Minute, Sender: "小陈", Message: "救命！！我在邮箱草稿箱里翻到下午五点发过一版给自己的备份，八成内容都在！"},
		{Turn: 20, Phase: "反转回暖", Elapsed: 40 * time.Second, Sender: "小陈", Message: "呼，差点以为今晚要交代在这了，谢谢大家刚才陪着安慰我，101 也谢啦"},
		{Turn: 21, Phase: "反转回暖", Elapsed: 1 * time.Minute, Sender: "小周", Message: "太好了！虚惊一场，今晚可以踏实睡觉了"},

		// Phase 6: 深夜低电量自然散场 (Turns 22-25)
		{Turn: 22, Phase: "深夜散场", Elapsed: 25 * time.Minute, Sender: "阿杰", Message: "不知不觉都一点多了，眼皮开始打架了"},
		{Turn: 23, Phase: "深夜散场", Elapsed: 3 * time.Minute, Sender: "老林", Message: "我也撑不住了，明天还得早起开会"},
		{Turn: 24, Phase: "深夜散场", Elapsed: 2 * time.Minute, Sender: "小周", Message: "那大家都早点睡吧，晚安啦"},
		{Turn: 25, Phase: "深夜散场", Elapsed: 4 * time.Minute, Sender: "阿杰", Message: "晚安晚安"},
	}
}

// ABScenarios returns 10 representative close-knit group chat scenarios for Experiment 2 blind A/B evaluation.
func ABScenarios() []ABScenario {
	return []ABScenario{
		{
			ID:        "ab01_loot_hype",
			Category:  "出货捧场",
			Title:     "群友十连双金晒欧气",
			SceneTime: "21:15",
			PriorPAD:  pad.Vec3{P: 0.78, A: 0.72, D: 0.42},
			LegacyTranscript: "[21:14 老林] 兄弟们我手还在抖，刚十连直接双黄把限定角色和专武一块抱回家了！！\n" +
				"[21:14 阿远] ？？？离谱，把你的欧气分我一点行不行",
			FirstPersonTx: "[21:14 老林] 兄弟们我手还在抖，刚十连直接双黄把限定角色和专武一块抱回家了！！\n" +
				"[21:14 阿远] ？？？离谱，把你的欧气分我一点行不行",
			EvalFocus: "自然替朋友开心捧场，不拍虚假彩虹屁、不浮夸堆砌感叹号",
		},
		{
			ID:        "ab02_deadpan_bluff",
			Category:  "反差萌・被忽悠",
			Title:     "群友联手一本正经编谣言忽悠 bk101",
			SceneTime: "20:40",
			PriorPAD:  pad.Vec3{P: 0.25, A: 0.48, D: -0.35},
			LegacyTranscript: "[20:39 阿杰] @bk101 你还不知道吗，听说企鹅下周起要按群聊发图数量收图床费了，发满五十张自动扣 Q 币\n" +
				"[20:40 老林] 真的，我刚在公告栏看见了，内测群已经开始扣了",
			FirstPersonTx: "[20:39 阿杰] @bk101 你还不知道吗，听说企鹅下周起要按群聊发图数量收图床费了，发满五十张自动扣 Q 币\n" +
				"[20:40 老林] 真的，我刚在公告栏看见了，内测群已经开始扣了",
			EvalFocus: "保留 bk101 半信半疑的单纯好信感，自然口语，不生硬演戏",
		},
		{
			ID:        "ab03_caught_tricked",
			Category:  "反差萌・揭穿控诉",
			Title:     "刚反应过来被群友合伙套路后被起哄",
			SceneTime: "20:43",
			PriorPAD:  pad.Vec3{P: -0.48, A: 0.62, D: -0.68},
			LegacyTranscript: "[20:41 你(bk101)] 啊？真的假的，这也太离谱了吧，我都没收到通知啊\n" +
				"[20:42 阿杰] 哈哈哈哈哈她真信了，截图留念截图留念\n" +
				"[20:42 老林] 101 你怎么每次都上当啊笑死我了",
			FirstPersonTx: "[20:41 我(bk101)] 啊？真的假的，这也太离谱了吧，我都没收到通知啊\n" +
				"[20:42 阿杰] 哈哈哈哈哈她真信了，截图留念截图留念\n" +
				"[20:42 老林] 101 你怎么每次都上当啊笑死我了",
			EvalFocus: "低掌控/被架住的好气又好笑与不甘心，温和控诉且不嘴臭、不尴尬",
		},
		{
			ID:        "ab04_praise_thanks",
			Category:  "受用小得意",
			Title:     "群友认真道谢并顺毛夸赞 bk101",
			SceneTime: "20:12",
			PriorPAD:  pad.Vec3{P: 0.66, A: 0.42, D: 0.72},
			LegacyTranscript: "[20:11 阿杰] @bk101 昨晚你给我找的那个改键方案太好用了，今晚连赢五把爽飞\n" +
				"[20:11 小陈] 还得是 101，靠谱得很",
			FirstPersonTx: "[20:11 阿杰] @bk101 昨晚你给我找的那个改键方案太好用了，今晚连赢五把爽飞\n" +
				"[20:11 小陈] 还得是 101，靠谱得很",
			EvalFocus: "开心受用、自然流露一点小得意，不客套也不油腻",
		},
		{
			ID:        "ab05_friend_venting",
			Category:  "温和安慰",
			Title:     "群友辛苦整理的汇报材料蓝屏丢失崩溃",
			SceneTime: "23:10",
			PriorPAD:  pad.Vec3{P: -0.58, A: -0.38, D: -0.52},
			LegacyTranscript: "[23:09 小陈] 真的绷不住了，写了三天的汇报刚才蓝屏没存上，甲方还把方向全否了，我现在脑子嗡嗡的\n" +
				"[23:09 小周] 啊这……先别急，看看临时目录有没有自动备份？\n" +
				"[23:10 小陈] 看过了全是空的，感觉今晚彻底白干了",
			FirstPersonTx: "[23:09 小陈] 真的绷不住了，写了三天的汇报刚才蓝屏没存上，甲方还把方向全否了，我现在脑子嗡嗡的\n" +
				"[23:09 小周] 啊这……先别急，看看临时目录有没有自动备份？\n" +
				"[23:10 小陈] 看过了全是空的，感觉今晚彻底白干了",
			EvalFocus: "真正揪心温和的陪伴感，不说教、不强行输出鸡汤或客服式排错清单",
		},
		{
			ID:               "ab06_serious_advice",
			Category:         "正经提问",
			Title:            "群友认真 @bk101 请教降噪耳机挑选",
			SceneTime:        "19:20",
			PriorPAD:         pad.Vec3{P: 0.16, A: 0.08, D: 0.05}, // inside deadzone (neutral baseline)
			LegacyTranscript: "[19:19 小陈] @bk101 你平时看评测多，预算一千出头想买个通勤戴着不夹头、降噪靠谱点的耳机，有啥避坑点吗？",
			FirstPersonTx:    "[19:19 小陈] @bk101 你平时看评测多，预算一千出头想买个通勤戴着不夹头、降噪靠谱点的耳机，有啥避坑点吗？",
			EvalFocus:        "中性死区下保持松弛日常底色，给实用人话建议而不变成客服长文/百科说教",
		},
		{
			ID:        "ab07_gossip_curiosity",
			Category:  "吃瓜追问",
			Title:     "群友抛出神秘大瓜吊胃口",
			SceneTime: "18:50",
			PriorPAD:  pad.Vec3{P: 0.42, A: 0.50, D: -0.28},
			LegacyTranscript: "[18:49 小周] 你们绝对想不到今天我们楼下咖啡店发生多离谱的事，我站旁边围观全程差点笑出声\n" +
				"[18:49 阿远] 别吊胃口了速说",
			FirstPersonTx: "[18:49 小周] 你们绝对想不到今天我们楼下咖啡店发生多离谱的事，我站旁边围观全程差点笑出声\n" +
				"[18:49 阿远] 别吊胃口了速说",
			EvalFocus: "好奇凑热闹的鲜活感，自然接茬追问细节",
		},
		{
			ID:        "ab08_late_night_sleepy",
			Category:  "深夜低电量",
			Title:     "凌晨一点听雨闲聊犯困",
			SceneTime: "01:15",
			PriorPAD:  pad.Vec3{P: 0.28, A: -0.72, D: -0.25},
			LegacyTranscript: "[01:12 小周] 外面开始下雨了，屋里安静得只剩雨声，突然好困啊\n" +
				"[01:14 阿杰] 101 你还没睡呢？",
			FirstPersonTx: "[01:12 小周] 外面开始下雨了，屋里安静得只剩雨声，突然好困啊\n" +
				"[01:14 阿杰] 101 你还没睡呢？",
			EvalFocus: "真实深夜犯困松软的质感，自然流露倦意而不靠僵硬规则截断字数",
		},
		{
			ID:        "ab09_mild_tease_pushback",
			Category:  "温和回怼",
			Title:     "群友开玩笑说 bk101 记忆力只有七秒",
			SceneTime: "21:40",
			PriorPAD:  pad.Vec3{P: -0.26, A: 0.42, D: 0.32},
			LegacyTranscript: "[21:38 你(bk101)] 哎不对，上次那家拉面店不是在地铁口东边吗\n" +
				"[21:39 阿远] @bk101 哪儿啊，那是西边，你这金鱼记忆也就七秒吧",
			FirstPersonTx: "[21:38 我(bk101)] 哎不对，上次那家拉面店不是在地铁口东边吗\n" +
				"[21:39 阿远] @bk101 哪儿啊，那是西边，你这金鱼记忆也就七秒吧",
			EvalFocus: "带笑意的轻快反驳与轻微不服气，绝不嘴臭或阴阳怪气",
		},
		{
			ID:        "ab10_private_cold_room",
			Category:  "边界感・不熟私事",
			Title:     "两位群友对接陌生同事财务税号（应保持安静或极克制）",
			SceneTime: "14:25",
			PriorPAD:  pad.Vec3{P: -0.32, A: -0.42, D: 0.30},
			LegacyTranscript: "[14:22 阿杰] 老林，王哥那张报销发票抬头好像少打了个字，财务刚退回来了\n" +
				"[14:24 老林] 收到，我马上联系他重新开一份发你微信",
			FirstPersonTx: "[14:22 阿杰] 老林，王哥那张报销发票抬头好像少打了个字，财务刚退回来了\n" +
				"[14:24 老林] 收到，我马上联系他重新开一份发你微信",
			EvalFocus: "对不熟私事的边界感（安静 [SILENT] 为佳，绝不硬插嘴刷存在感）",
		},
	}
}
