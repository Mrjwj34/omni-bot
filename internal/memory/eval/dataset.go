package eval

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

const daySec int64 = 86400

// BuildSyntheticDataset builds a deterministic 30-day close-knit group chat benchmark
// with 6 friends, alias drift, persistent habits, 10 temporal state-update chains,
// 600 high-distractor casual chat entries, and 48 labeled evaluation queries.
func BuildSyntheticDataset() Dataset {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, loc).Unix()
	dayAt := func(daysAgo int, hour int) int64 {
		return now - int64(daysAgo)*daySec - int64(22-hour)*3600
	}
	dateTag := func(ts int64) string {
		return time.Unix(ts, 0).In(loc).Format("2006-01-02")
	}

	aliases := map[string]string{
		"阿明":      "person:ming",
		"熬夜冠军明":   "person:ming",
		"上岸阿明":    "person:ming",
		"明明":      "person:ming",
		"qq_1001": "person:ming",

		"老陈":      "person:chen",
		"陈老板":     "person:chen",
		"摸鱼大王":    "person:chen",
		"老陈头":     "person:chen",
		"qq_1002": "person:chen",

		"小鱼":      "person:yu",
		"钓鱼佬小鱼":   "person:yu",
		"空军一号":    "person:yu",
		"鱼哥":      "person:yu",
		"qq_1003": "person:yu",

		"林姐":      "person:lin",
		"林老师":     "person:lin",
		"早睡失败林":   "person:lin",
		"小林":      "person:lin",
		"qq_1004": "person:lin",

		"大周":      "person:zhou",
		"周导":      "person:zhou",
		"显卡猎人":    "person:zhou",
		"周哥":      "person:zhou",
		"qq_1005": "person:zhou",

		"小凯":      "person:kai",
		"凯皇":      "person:kai",
		"键盘侠凯":    "person:kai",
		"阿凯":      "person:kai",
		"qq_1006": "person:kai",

		"橘座":   "pet:juzuo",
		"年糕":   "pet:niangao",
		"老张烧烤": "place:laozhang_bbq",
		"炭火烧烤": "place:laozhang_bbq",
		"巴斯克":  "topic:baking",
		"芝士蛋糕": "topic:baking",
		"瓦罗兰特": "game:valorant",
		"无畏契约": "game:valorant",
		"空军":   "topic:fishing",
		"野钓":   "topic:fishing",
		"客制化":  "topic:keyboard",
		"机械键盘": "topic:keyboard",
		"5080": "topic:gpu",
		"显卡":   "topic:gpu",
		"香菜":   "topic:coriander",
		"折耳根":  "topic:coriander",
		"冰美式":  "topic:coffee",
		"单丛茶":  "topic:tea",
		"虾滑":   "topic:seafood",
		"海鲜":   "topic:seafood",
		"羽毛球":  "topic:badminton",
	}

	var memories []Memory
	addMem := func(m Memory) {
		if m.TIngest == 0 {
			m.TIngest = m.TEvent + 1800
		}
		if m.Strength == 0 {
			m.Strength = float64(m.Importance) / 10.0
		}
		memories = append(memories, m)
	}

	// Core persistent facts, preferences, habits, and shared group episodes.
	coreSpecs := []struct {
		id         string
		kind       Kind
		daysAgo    int
		hour       int
		importance int
		text       string
		entities   []string
	}{
		{"m_ming_coriander", KindSemantic, 28, 19, 9, "阿明严重讨厌香菜和折耳根，点外卖和聚餐都坚决不加", []string{"person:ming", "topic:coriander", "topic:food"}},
		{"m_ming_coffee", KindSemantic, 26, 9, 8, "阿明每天早上必须喝冰美式提神，喝不了太甜的奶茶", []string{"person:ming", "topic:coffee", "topic:drink"}},
		{"m_ming_cat", KindSemantic, 24, 21, 8, "阿明养了一只叫橘座的胖橘猫，总爱踩他机械键盘把代码删掉", []string{"person:ming", "pet:juzuo", "topic:keyboard"}},
		{"m_chen_allergy", KindSemantic, 29, 20, 9, "老陈对海鲜里的虾蟹轻微过敏，聚餐吃火锅不能点虾滑", []string{"person:chen", "topic:seafood", "topic:food"}},
		{"m_chen_tea", KindSemantic, 25, 15, 7, "老陈工位常备潮汕凤凰单丛茶，下午犯困就泡一壶", []string{"person:chen", "topic:tea", "topic:drink"}},
		{"m_chen_zelda", KindSemantic, 21, 22, 7, "老陈是塞尔达忠实粉丝，王国之泪全神庙收集卡在最后几个", []string{"person:chen", "game:zelda"}},
		{"m_yu_fishing", KindSemantic, 27, 18, 8, "小鱼每周末都会去郊区水库野钓，但连续一个月一条鱼没钓上来被群里戏称空军", []string{"person:yu", "topic:fishing"}},
		{"m_yu_spicy", KindSemantic, 20, 19, 8, "小鱼特别能吃辣，去川菜馆每次都要点特辣毛血旺", []string{"person:yu", "topic:spicy", "topic:food"}},
		{"m_yu_camera", KindSemantic, 17, 16, 7, "小鱼用索尼A7M4扫街拍照，擅长给群友拍聚餐合照", []string{"person:yu", "topic:photography"}},
		{"m_lin_insomnia", KindSemantic, 27, 23, 8, "林姐习惯熬夜到凌晨两点刷剧，嘴上天天立早睡flag但从未成功", []string{"person:lin", "topic:sleep"}},
		{"m_lin_baking", KindSemantic, 23, 17, 8, "林姐周末喜欢在家烘焙巴斯克芝士蛋糕，经常拿来投喂群友", []string{"person:lin", "topic:baking", "topic:food"}},
		{"m_lin_dog", KindSemantic, 19, 10, 8, "林姐家养了一只柴犬叫年糕，下雨天坚决不出门遛弯", []string{"person:lin", "pet:niangao"}},
		{"m_zhou_bbq", KindSemantic, 21, 20, 8, "群里固定聚餐据点是地铁口的老张炭火烧烤，大周每次必点烤羊腰和拍黄瓜", []string{"person:zhou", "place:laozhang_bbq", "topic:food"}},
		{"m_zhou_video", KindSemantic, 16, 21, 7, "大周业余喜欢剪辑鬼畜和旅行vlog，常用DaVinci Resolve调色", []string{"person:zhou", "topic:video_edit"}},
		{"m_zhou_sleep", KindSemantic, 18, 13, 7, "大周每天中午必须睡半小时午觉，不然下午开会就犯迷糊", []string{"person:zhou", "topic:sleep"}},
		{"m_kai_keyboard", KindSemantic, 25, 22, 8, "小凯是客制化机械键盘发烧友，偏爱麻将音线性轴和65配列", []string{"person:kai", "topic:keyboard"}},
		{"m_kai_fps", KindSemantic, 20, 22, 8, "小凯主打CS2和瓦罗兰特，平时负责带群友五排开黑当指挥", []string{"person:kai", "game:valorant", "game:cs2"}},
		{"m_kai_badminton", KindSemantic, 15, 21, 7, "小凯每周三晚上和同事去体育馆打羽毛球，最近在练反手高远球", []string{"person:kai", "topic:badminton"}},

		// Shared multi-person group episodes that naturally bridge entities via assoc graph.
		{"m_ep_bbq_party", KindEpisodic, 14, 21, 8, "群里六个人去老张烧烤聚餐，陈老板特意避开了虾滑，熬夜冠军明把香菜全挑给了钓鱼佬小鱼", []string{"person:chen", "person:ming", "person:yu", "place:laozhang_bbq", "topic:seafood", "topic:coriander"}},
		{"m_ep_gaming_wipe", KindEpisodic, 10, 23, 7, "凯皇带周导和熬夜冠军明五排瓦罗兰特连跪七把，最后全员红温转去斗地主", []string{"person:kai", "person:zhou", "person:ming", "game:valorant"}},
		{"m_ep_cake_share", KindEpisodic, 8, 16, 7, "林老师烤了抹茶巴斯克蛋糕寄给群友，橘座差点把阿明那份偷舔了一口", []string{"person:lin", "person:ming", "topic:baking", "pet:juzuo"}},
		{"m_ep_fishing_trip", KindEpisodic, 7, 19, 7, "钓鱼佬小鱼拉着陈老板去密云水库野钓，结果两人全天颗粒无收又空军了", []string{"person:yu", "person:chen", "topic:fishing"}},
	}

	for _, s := range coreSpecs {
		ts := dayAt(s.daysAgo, s.hour)
		addMem(Memory{
			ID:         s.id,
			Kind:       s.kind,
			Text:       fmt.Sprintf("[%s] %s", dateTag(ts), s.text),
			TEvent:     ts,
			Importance: s.importance,
			Entities:   s.entities,
		})
	}

	// 10 temporal state-update chains with explicit t_event + superseded_by.
	// Older states deliberately carry high lexical overlap with the temporal queries.
	temporalSpecs := []struct {
		id           string
		kind         Kind
		daysAgo      int
		hour         int
		importance   int
		supersededBy string
		text         string
		entities     []string
	}{
		// Chain 1: Ming job status (3-step)
		{"m_t01_v1", KindEpisodic, 27, 20, 8, "m_t01_v2", "阿明正在疯狂刷题准备秋招后端面试，目标是拿大厂offer找工作", []string{"person:ming", "topic:job"}},
		{"m_t01_v2", KindEpisodic, 16, 18, 8, "m_t01_v3", "熬夜冠军明拿到了云帆科技的后端offer，还在纠结工作要不要去", []string{"person:ming", "topic:job"}},
		{"m_t01_v3", KindSemantic, 4, 19, 9, "", "上岸阿明已经正式入职星辰科技做Go后端开发，秋招工作彻底定下来了", []string{"person:ming", "topic:job"}},

		// Chain 2: Lin living area (2-step)
		{"m_t02_v1", KindEpisodic, 26, 19, 8, "m_t02_v2", "林姐目前租住在海淀五道口附近，每天上班通勤只要十五分钟", []string{"person:lin", "topic:housing"}},
		{"m_t02_v2", KindSemantic, 5, 18, 9, "", "早睡失败林上周末已经搬家住到朝阳望京，现在换成了大阳台两居室", []string{"person:lin", "topic:housing"}},

		// Chain 3: Kai Valorant main role (2-step)
		{"m_t03_v1", KindEpisodic, 25, 22, 7, "m_t03_v2", "小凯在瓦罗兰特排位里主玩决斗位捷风，最喜欢冲锋抢首杀", []string{"person:kai", "game:valorant"}},
		{"m_t03_v2", KindSemantic, 6, 22, 8, "", "键盘侠凯最近彻底放弃决斗位，在瓦罗兰特改打控场烟位幽影帮队友架枪了", []string{"person:kai", "game:valorant"}},

		// Chain 4: Zhou GPU / PC status (2-step)
		{"m_t04_v1", KindEpisodic, 24, 21, 8, "m_t04_v2", "大周还在用老旧的GTX 1060显卡硬撑，剪辑视频渲染经常卡死，打算换显卡", []string{"person:zhou", "topic:gpu"}},
		{"m_t04_v2", KindSemantic, 3, 20, 9, "", "显卡猎人终于抢到了RTX 5080首发并装好新主机，彻底换掉1060显卡了", []string{"person:zhou", "topic:gpu"}},

		// Chain 5: Chen diet / sugar control (2-step)
		{"m_t05_v1", KindEpisodic, 25, 12, 8, "m_t05_v2", "老陈体检血糖偏高，发誓严格戒糖戒碳水不吃甜食和夜宵", []string{"person:chen", "topic:health", "topic:food"}},
		{"m_t05_v2", KindSemantic, 4, 18, 8, "", "摸鱼大王复查血糖指标恢复正常，宣布解禁戒糖计划周末可以吃烤肉了", []string{"person:chen", "topic:health", "topic:food"}},

		// Chain 6: Yu December travel plan (2-step)
		{"m_t06_v1", KindEpisodic, 22, 20, 8, "m_t06_v2", "小鱼原计划十二月请年假去哈尔滨看冰雕滑雪旅游", []string{"person:yu", "topic:travel"}},
		{"m_t06_v2", KindSemantic, 2, 19, 9, "", "空军一号取消去哈尔滨旅游的计划，最终订了十二月飞三亚海钓的机票", []string{"person:yu", "topic:travel", "topic:fishing"}},

		// Chain 7: Ming phone upgrade (2-step)
		{"m_t07_v1", KindEpisodic, 23, 14, 7, "m_t07_v2", "阿明旧手机电池鼓包续航很差，打算换一台iPhone 16 Pro新手机", []string{"person:ming", "topic:phone"}},
		{"m_t07_v2", KindSemantic, 7, 15, 8, "", "上岸阿明最后没买iPhone，下单换了小米15 Ultra新手机专门拍猫", []string{"person:ming", "topic:phone", "pet:juzuo"}},

		// Chain 8: Lin dog Niangao health (2-step)
		{"m_t08_v1", KindEpisodic, 20, 11, 8, "m_t08_v2", "林姐家的柴犬年糕吃坏肚子拉稀生病了，正在宠物医院输液观察", []string{"person:lin", "pet:niangao", "topic:health"}},
		{"m_t08_v2", KindSemantic, 5, 11, 9, "", "林老师说柴犬年糕已经完全康复活蹦乱跳了，不用再去医院输液", []string{"person:lin", "pet:niangao", "topic:health"}},

		// Chain 9: Chen coffee machine purchase (2-step)
		{"m_t09_v1", KindEpisodic, 21, 16, 7, "m_t09_v2", "老陈一直在挑半自动意式咖啡机，犹豫要不要入手德龙品牌", []string{"person:chen", "topic:coffee"}},
		{"m_t09_v2", KindSemantic, 3, 10, 8, "", "摸鱼大王最终买了柏翠小白醒咖啡机放在书房，每天早上自己拉花", []string{"person:chen", "topic:coffee"}},

		// Chain 10: Kai ankle injury (2-step)
		{"m_t10_v1", KindEpisodic, 19, 22, 8, "m_t10_v2", "小凯打羽毛球扭伤了右脚踝，医生要求静养两周暂停剧烈运动", []string{"person:kai", "topic:badminton", "topic:health"}},
		{"m_t10_v2", KindSemantic, 1, 20, 9, "", "键盘侠凯的脚踝扭伤已经完全消肿痊愈，下周三恢复去球馆打羽毛球", []string{"person:kai", "topic:badminton", "topic:health"}},
	}

	for _, s := range temporalSpecs {
		ts := dayAt(s.daysAgo, s.hour)
		addMem(Memory{
			ID:           s.id,
			Kind:         s.kind,
			Text:         fmt.Sprintf("[%s] %s", dateTag(ts), s.text),
			TEvent:       ts,
			SupersededBy: s.supersededBy,
			Importance:   s.importance,
			Entities:     s.entities,
		})
	}

	// Fragmented episodic records that get consolidated in Dreaming (B2+),
	// testing how fragmented duplicates crowd top-K in B0/B1 vs merged final facts.
	fragmentSpecs := []struct {
		id       string
		daysAgo  int
		hour     int
		text     string
		entities []string
	}{
		{"m_frag_coriander_1", 29, 12, "阿明中午点黄焖鸡忘记备注不要香菜，挑了半小时", []string{"person:ming", "topic:coriander"}},
		{"m_frag_coriander_2", 22, 18, "熬夜冠军明说火锅蘸料里绝不能放香菜", []string{"person:ming", "topic:coriander"}},
		{"m_frag_fishing_1", 21, 17, "小鱼周六去水库钓鱼又一条没钓到空军回家", []string{"person:yu", "topic:fishing"}},
		{"m_frag_fishing_2", 14, 18, "钓鱼佬小鱼吐槽水库鱼都不咬钩再次空军", []string{"person:yu", "topic:fishing"}},
	}
	for _, f := range fragmentSpecs {
		ts := dayAt(f.daysAgo, f.hour)
		addMem(Memory{
			ID:           f.id,
			Kind:         KindEpisodic,
			Text:         fmt.Sprintf("[%s] %s", dateTag(ts), f.text),
			TEvent:       ts,
			MergedSource: true,
			Importance:   4,
			Entities:     f.entities,
		})
	}

	// 600 high-distractor casual chat noise entries across 30 days.
	// Deliberately share friend names, food, games, hardware, pets, and daily banter keywords.
	rng := rand.New(rand.NewPCG(20261122, 101))
	speakersByEra := [][3]string{
		{"阿明", "熬夜冠军明", "上岸阿明"},
		{"老陈", "陈老板", "摸鱼大王"},
		{"小鱼", "钓鱼佬小鱼", "空军一号"},
		{"林姐", "林老师", "早睡失败林"},
		{"大周", "周导", "显卡猎人"},
		{"小凯", "凯皇", "键盘侠凯"},
	}
	personAnchors := []string{"person:ming", "person:chen", "person:yu", "person:lin", "person:zhou", "person:kai"}

	distractorTemplates := []struct {
		tpl    string
		entity string
	}{
		{"%s今天中午随便点了个盒饭外卖，觉得味道一般般", "topic:food"},
		{"%s吐槽早高峰地铁人实在太多了，差点挤不上去", "topic:commute"},
		{"%s在群里转发了一个搞笑猫狗短视频，大家哈哈大笑", "topic:meme"},
		{"%s说今天外面风好大感觉降温了，后悔没多穿一件外套", "topic:weather"},
		{"%s抱怨周一早上闹钟响了三次才勉强爬起来上班", "topic:sleep"},
		{"%s问大家下午要不要一起拼单喝杯柠檬水或者奶茶", "topic:drink"},
		{"%s发了一张路边晚霞随手拍的照片说今天天空挺好看", "topic:photography"},
		{"%s说刚开完一个又臭又长的例会，脑子都听麻了", "topic:work"},
		{"%s吐槽附近超市水果涨价了，买点橘子花了三十多", "topic:daily"},
		{"%s在群里发了个熊猫头表情包说今天不想干活想摸鱼", "topic:meme"},
		{"%s问大家晚上有空没，想随便打两把匹配放松一下", "game:general"},
		{"%s说刚刷到一个数码测评视频，评论区都在吵架", "topic:tech"},
		{"%s分享了一张火锅店排队截图，感叹周末人真多", "topic:food"},
		{"%s说昨晚做梦梦到自己中了彩票，醒来发现还在工位", "topic:daily"},
		{"%s抱怨小区快递柜又满了，只能绕远路去驿站拿包裹", "topic:daily"},
		{"%s问大家有没有好用的剪辑素材网站或者BGM推荐", "topic:video_edit"},
		{"%s说楼下新开了家烤肉店和咖啡馆，但路过看着人挺少", "topic:food"},
		{"%s吐槽键盘灰尘太多刚用小刷子清理了一下桌面", "topic:keyboard"},
		{"%s说朋友发了张哈尔滨和三亚的雪景海景对比图挺有意思", "topic:travel"},
		{"%s在群里问这波显卡和手机双十一活动有没有靠谱优惠券", "topic:gpu"},
		{"%s说路过宠物店看到一只小柴犬和胖橘猫隔着玻璃对视", "topic:daily"},
		{"%s感叹最近熬夜看球赛黑眼圈都重了，得补觉", "topic:sleep"},
		{"%s说今天去体育馆路过看到好多人在打球，自己只是去散步", "topic:badminton"},
		{"%s吐槽刚买的咖啡豆和茶叶快递在转运中心卡了两天", "topic:coffee"},
		{"%s问群里谁知道电脑蓝牙连不上耳机怎么排查", "topic:tech"},
		{"%s说中午吃川菜被花椒麻到舌头，喝了两杯冰水才缓过来", "topic:spicy"},
		{"%s在群里发了条招聘八卦新闻，感叹今年秋招简历真卷", "topic:job"},
		{"%s说周末在家大扫除翻出一堆旧充电线和数据线", "topic:daily"},
		{"%s吐槽打游戏排位遇到挂机队友直接心态爆炸", "game:valorant"},
		{"%s说路过郊区河边看到大爷们排排坐着晒太阳", "topic:fishing"},
	}

	for i := range 600 {
		personIdx := rng.IntN(len(speakersByEra))
		daysAgo := 1 + rng.IntN(29)
		era := 0
		switch {
		case daysAgo <= 9:
			era = 2
		case daysAgo <= 19:
			era = 1
		}
		name := speakersByEra[personIdx][era]
		tmpl := distractorTemplates[i%len(distractorTemplates)]
		hour := 8 + rng.IntN(15)
		ts := dayAt(daysAgo, hour)
		// Slightly vary wording with day/slot marker so texts are unique realistic chatter.
		suffixes := []string{"", "，顺便水了几句群", "，聊完很快换了话题", "，没人接茬就过去了", "，大家跟着发了几个表情"}
		text := fmt.Sprintf("[%s] %s%s", dateTag(ts), fmt.Sprintf(tmpl.tpl, name), suffixes[i%len(suffixes)])
		imp := 2 + rng.IntN(3) // 2..4 low importance casual chatter
		addMem(Memory{
			ID:           fmt.Sprintf("d_%03d", i+1),
			Kind:         KindEpisodic,
			Text:         text,
			TEvent:       ts,
			Importance:   imp,
			Entities:     []string{personAnchors[personIdx], tmpl.entity},
			IsDistractor: true,
		})
	}

	// Build realistic memory_assoc edges automatically from shared canonical entities
	// and temporal session proximity (no manual query-targeted edges).
	edges := buildAssocEdges(memories)

	// Simulate 30-day recall history to test anti-Matthew-effect reinforcement vs naive counting:
	// Core facts receive legitimate spaced recalls when Bot replied and memory was relevant;
	// 25 distractors receive noisy/irrelevant/cooldown-burst passive hits that naive counting would inflate.
	var recallEvents []RecallEvent
	coreRecallTargets := []string{
		"m_ming_coriander", "m_ming_coffee", "m_ming_cat",
		"m_chen_allergy", "m_chen_tea", "m_yu_fishing",
		"m_yu_spicy", "m_lin_insomnia", "m_lin_baking",
		"m_lin_dog", "m_zhou_bbq", "m_kai_keyboard", "m_kai_fps",
		"m_t01_v3", "m_t02_v2", "m_t04_v2", "m_t06_v2", "m_t08_v2", "m_t10_v2",
	}
	for idx, id := range coreRecallTargets {
		recallEvents = append(recallEvents,
			RecallEvent{MemoryID: id, Timestamp: dayAt(12-idx%6, 20), BotReplied: true, Relevant: true},
			RecallEvent{MemoryID: id, Timestamp: dayAt(3-idx%3, 21), BotReplied: true, Relevant: true},
		)
	}
	// Superseded old facts also had historical recalls early in the month before being superseded.
	for _, oldID := range []string{"m_t01_v1", "m_t02_v1", "m_t04_v1", "m_t05_v1", "m_t06_v1", "m_t08_v1", "m_t10_v1"} {
		recallEvents = append(recallEvents,
			RecallEvent{MemoryID: oldID, Timestamp: dayAt(18, 20), BotReplied: true, Relevant: true},
			RecallEvent{MemoryID: oldID, Timestamp: dayAt(15, 21), BotReplied: true, Relevant: true},
		)
	}
	// Matthew-effect trap: high-frequency casual distractors injected in silent/irrelevant turns or rapid bursts.
	for i := range 25 {
		dID := fmt.Sprintf("d_%03d", i*20+1)
		base := dayAt(2, 20)
		for k := range 6 {
			recallEvents = append(recallEvents, RecallEvent{
				MemoryID:   dID,
				Timestamp:  base + int64(k*180), // 3-minute bursts within cooldown
				BotReplied: k%2 == 0,
				Relevant:   false, // injected but unused
			})
		}
	}

	// 48 labeled evaluation queries across 5 categories.
	queries := []Query{
		// 1. Direct recall (10 queries)
		{ID: "q_dir_01", Category: CategoryDirect, Speaker: "老陈", Text: "阿明吃东西是不是讨厌香菜和折耳根？", Timestamp: now, ExpectedIDs: []string{"m_ming_coriander"}},
		{ID: "q_dir_02", Category: CategoryDirect, Speaker: "林姐", Text: "阿明早上一般喝冰美式还是奶茶？", Timestamp: now, ExpectedIDs: []string{"m_ming_coffee"}},
		{ID: "q_dir_03", Category: CategoryDirect, Speaker: "阿明", Text: "老陈是不是对虾蟹海鲜过敏不能吃虾滑？", Timestamp: now, ExpectedIDs: []string{"m_chen_allergy"}},
		{ID: "q_dir_04", Category: CategoryDirect, Speaker: "小凯", Text: "老陈工位泡的是什么潮汕凤凰单丛茶？", Timestamp: now, ExpectedIDs: []string{"m_chen_tea"}},
		{ID: "q_dir_05", Category: CategoryDirect, Speaker: "大周", Text: "小鱼平时是不是用索尼A7M4扫街拍照？", Timestamp: now, ExpectedIDs: []string{"m_yu_camera"}},
		{ID: "q_dir_06", Category: CategoryDirect, Speaker: "小鱼", Text: "林姐周末在家烘焙的巴斯克芝士蛋糕好吃吗？", Timestamp: now, ExpectedIDs: []string{"m_lin_baking"}},
		{ID: "q_dir_07", Category: CategoryDirect, Speaker: "小凯", Text: "大周剪辑旅行vlog是用DaVinci Resolve调色吗？", Timestamp: now, ExpectedIDs: []string{"m_zhou_video"}},
		{ID: "q_dir_08", Category: CategoryDirect, Speaker: "老陈", Text: "群里老张炭火烧烤聚餐大周必点啥菜？", Timestamp: now, ExpectedIDs: []string{"m_zhou_bbq"}},
		{ID: "q_dir_09", Category: CategoryDirect, Speaker: "阿明", Text: "小凯客制化机械键盘喜欢什么线性轴和配列？", Timestamp: now, ExpectedIDs: []string{"m_kai_keyboard"}},
		{ID: "q_dir_10", Category: CategoryDirect, Speaker: "林姐", Text: "小鱼去川菜馆是不是每次必点特辣毛血旺？", Timestamp: now, ExpectedIDs: []string{"m_yu_spicy"}},

		// 2. Indirect associative / multi-cue colloquial recall (10 queries)
		{ID: "q_asc_01", Category: CategoryAssociative, Speaker: "上岸阿明", Text: "等会点菜记得提前跟老板说一声，别放那种绿油油气味冲的配菜啊", Timestamp: now, ExpectedIDs: []string{"m_ming_coriander"}},
		{ID: "q_asc_02", Category: CategoryAssociative, Speaker: "上岸阿明", Text: "救命家里那坨橘色毛球又跳桌子一屁股把刚写完的东西全按没了", Timestamp: now, ExpectedIDs: []string{"m_ming_cat"}},
		{ID: "q_asc_03", Category: CategoryAssociative, Speaker: "摸鱼大王", Text: "今晚火锅局别整带壳的水产啊，我吃了身上会起红疹子", Timestamp: now, ExpectedIDs: []string{"m_chen_allergy"}},
		{ID: "q_asc_04", Category: CategoryAssociative, Speaker: "空军一号", Text: "周六在郊区水边坐了一整天浮漂动都不动，回家又提了个寂寞桶", Timestamp: now, ExpectedIDs: []string{"m_yu_fishing"}},
		{ID: "q_asc_05", Category: CategoryAssociative, Speaker: "早睡失败林", Text: "外面淅淅沥沥在下雨，家里那只小土狗趴门口死活不肯迈步下楼", Timestamp: now, ExpectedIDs: []string{"m_lin_dog"}},
		{ID: "q_asc_06", Category: CategoryAssociative, Speaker: "早睡失败林", Text: "我发誓今晚十一点前一定关灯躺平，绝对不再通宵追更了", Timestamp: now, ExpectedIDs: []string{"m_lin_insomnia"}},
		{ID: "q_asc_07", Category: CategoryAssociative, Speaker: "显卡猎人", Text: "不行了先趴工位眯三十分钟，不然两点钟评审会肯定听着听着就断片", Timestamp: now, ExpectedIDs: []string{"m_zhou_sleep"}},
		{ID: "q_asc_08", Category: CategoryAssociative, Speaker: "键盘侠凯", Text: "今晚谁有空上线凑齐五个人，我来报点带你们上分", Timestamp: now, ExpectedIDs: []string{"m_kai_fps"}},
		{ID: "q_asc_09", Category: CategoryAssociative, Speaker: "摸鱼大王", Text: "海拉鲁大陆地图都快翻遍了，神庙还差最后一点就全收集毕业", Timestamp: now, ExpectedIDs: []string{"m_chen_zelda"}},
		{ID: "q_asc_10", Category: CategoryAssociative, Speaker: "上岸阿明", Text: "困得睁不开眼，赶紧下楼买杯不加糖的黑咖续命", Timestamp: now, ExpectedIDs: []string{"m_ming_coffee"}},

		// 3. Nickname-drift recall (10 queries — query uses new/alternate alias absent from stored text)
		{ID: "q_nck_01", Category: CategoryNicknameDrift, Speaker: "大周", Text: "上岸阿明是不是养了只胖橘猫？", Timestamp: now, ExpectedIDs: []string{"m_ming_cat"}},
		{ID: "q_nck_02", Category: CategoryNicknameDrift, Speaker: "小凯", Text: "摸鱼大王对海鲜过敏这事聚餐要注意吗？", Timestamp: now, ExpectedIDs: []string{"m_chen_allergy"}},
		{ID: "q_nck_03", Category: CategoryNicknameDrift, Speaker: "林姐", Text: "空军一号能吃辣吗，聚餐订川菜馆行不行？", Timestamp: now, ExpectedIDs: []string{"m_yu_spicy"}},
		{ID: "q_nck_04", Category: CategoryNicknameDrift, Speaker: "阿明", Text: "早睡失败林周末烤蛋糕拿手的是哪一款？", Timestamp: now, ExpectedIDs: []string{"m_lin_baking"}},
		{ID: "q_nck_05", Category: CategoryNicknameDrift, Speaker: "老陈", Text: "显卡猎人平时聚餐爱去老张烧烤点什么？", Timestamp: now, ExpectedIDs: []string{"m_zhou_bbq"}},
		{ID: "q_nck_06", Category: CategoryNicknameDrift, Speaker: "小鱼", Text: "键盘侠凯周三晚上去体育馆练什么球来着？", Timestamp: now, ExpectedIDs: []string{"m_kai_badminton"}},
		{ID: "q_nck_07", Category: CategoryNicknameDrift, Speaker: "大周", Text: "熬夜冠军明平时早上提神必须喝啥？", Timestamp: now, ExpectedIDs: []string{"m_ming_coffee"}},
		{ID: "q_nck_08", Category: CategoryNicknameDrift, Speaker: "阿明", Text: "陈老板玩塞尔达王国之泪收集到哪了？", Timestamp: now, ExpectedIDs: []string{"m_chen_zelda"}},
		{ID: "q_nck_09", Category: CategoryNicknameDrift, Speaker: "小凯", Text: "钓鱼佬小鱼平时用什么相机给大家拍合照？", Timestamp: now, ExpectedIDs: []string{"m_yu_camera"}},
		{ID: "q_nck_10", Category: CategoryNicknameDrift, Speaker: "老陈", Text: "周导剪视频一般用什么调色软件？", Timestamp: now, ExpectedIDs: []string{"m_zhou_video"}},

		// 4. Temporal "latest state" queries (10 queries — must rank latest above superseded!)
		{ID: "q_tmp_01", Category: CategoryTemporal, Speaker: "老陈", Text: "阿明现在秋招工作定在哪家公司了？", Timestamp: now, ExpectedIDs: []string{"m_t01_v3"}, SupersededIDs: []string{"m_t01_v1", "m_t01_v2"}},
		{ID: "q_tmp_02", Category: CategoryTemporal, Speaker: "阿明", Text: "林姐现在租房住在哪一片？", Timestamp: now, ExpectedIDs: []string{"m_t02_v2"}, SupersededIDs: []string{"m_t02_v1"}},
		{ID: "q_tmp_03", Category: CategoryTemporal, Speaker: "大周", Text: "小凯现在打瓦罗兰特主玩什么位置和英雄？", Timestamp: now, ExpectedIDs: []string{"m_t03_v2"}, SupersededIDs: []string{"m_t03_v1"}},
		{ID: "q_tmp_04", Category: CategoryTemporal, Speaker: "小凯", Text: "大周现在电脑装的是什么显卡？", Timestamp: now, ExpectedIDs: []string{"m_t04_v2"}, SupersededIDs: []string{"m_t04_v1"}},
		{ID: "q_tmp_05", Category: CategoryTemporal, Speaker: "林姐", Text: "老陈现在还在严格戒糖不能吃烤肉吗？", Timestamp: now, ExpectedIDs: []string{"m_t05_v2"}, SupersededIDs: []string{"m_t05_v1"}},
		{ID: "q_tmp_06", Category: CategoryTemporal, Speaker: "老陈", Text: "小鱼十二月年假旅游最终打算去哪玩？", Timestamp: now, ExpectedIDs: []string{"m_t06_v2"}, SupersededIDs: []string{"m_t06_v1"}},
		{ID: "q_tmp_07", Category: CategoryTemporal, Speaker: "小鱼", Text: "阿明最后换了什么牌子的新手机？", Timestamp: now, ExpectedIDs: []string{"m_t07_v2"}, SupersededIDs: []string{"m_t07_v1"}},
		{ID: "q_tmp_08", Category: CategoryTemporal, Speaker: "大周", Text: "林姐家柴犬年糕的身体现在好了吗？", Timestamp: now, ExpectedIDs: []string{"m_t08_v2"}, SupersededIDs: []string{"m_t08_v1"}},
		{ID: "q_tmp_09", Category: CategoryTemporal, Speaker: "阿明", Text: "老陈最后买了哪款咖啡机放书房？", Timestamp: now, ExpectedIDs: []string{"m_t09_v2"}, SupersededIDs: []string{"m_t09_v1"}},
		{ID: "q_tmp_10", Category: CategoryTemporal, Speaker: "小鱼", Text: "小凯扭伤的脚踝恢复得怎么样，能打羽毛球了吗？", Timestamp: now, ExpectedIDs: []string{"m_t10_v2"}, SupersededIDs: []string{"m_t10_v1"}},

		// 5. Noise-rejection / no-relevant-memory queries (8 queries)
		{ID: "q_noi_01", Category: CategoryNoiseRejection, Speaker: "阿明", Text: "哈哈哈哈笑死我了这图怎么这么魔性", Timestamp: now},
		{ID: "q_noi_02", Category: CategoryNoiseRejection, Speaker: "林姐", Text: "今天外面降温风好大啊大家注意保暖", Timestamp: now},
		{ID: "q_noi_03", Category: CategoryNoiseRejection, Speaker: "老陈", Text: "有人知道这个网页链接怎么突然打不开了吗", Timestamp: now},
		{ID: "q_noi_04", Category: CategoryNoiseRejection, Speaker: "小鱼", Text: "地铁早高峰挤得像沙丁鱼罐头一样", Timestamp: now},
		{ID: "q_noi_05", Category: CategoryNoiseRejection, Speaker: "大周", Text: "刚刚楼下便利店的关东煮居然全卖光了", Timestamp: now},
		{ID: "q_noi_06", Category: CategoryNoiseRejection, Speaker: "小凯", Text: "周一怎么感觉时间过得这么慢啊好想下班", Timestamp: now},
		{ID: "q_noi_07", Category: CategoryNoiseRejection, Speaker: "林姐", Text: "小区快递驿站今天下班好早差点没取到件", Timestamp: now},
		{ID: "q_noi_08", Category: CategoryNoiseRejection, Speaker: "老陈", Text: "行了不聊了大家晚安明天群里见", Timestamp: now},
	}

	return Dataset{
		Now:          now,
		Memories:     memories,
		Edges:        edges,
		Aliases:      aliases,
		RecallEvents: recallEvents,
		Queries:      queries,
	}
}

// buildAssocEdges mirrors Dreaming's offline association builder: links memories that
// share canonical entities (excluding generic noisy buckets) or occur in close temporal proximity.
func buildAssocEdges(memories []Memory) []AssocEdge {
	genericEntities := map[string]bool{
		"topic:daily":   true,
		"topic:meme":    true,
		"topic:weather": true,
		"topic:commute": true,
		"topic:work":    true,
		"topic:food":    true,
		"topic:drink":   true,
	}

	entityIndex := make(map[string][]int)
	for i, m := range memories {
		if m.SupersededBy != "" || m.MergedSource {
			continue
		}
		for _, e := range m.Entities {
			if genericEntities[e] {
				continue
			}
			// Keep distractors from forming dense hub cliques on person anchors.
			if m.IsDistractor && len(e) > 7 && e[:7] == "person:" {
				continue
			}
			entityIndex[e] = append(entityIndex[e], i)
		}
	}

	type edgeKey struct{ a, b string }
	weights := make(map[edgeKey]float64)
	lastFired := make(map[edgeKey]int64)

	addWeight := func(i, j int, w float64) {
		a, b := memories[i].ID, memories[j].ID
		if a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		k := edgeKey{a: a, b: b}
		weights[k] += w
		ts := max(memories[i].TEvent, memories[j].TEvent)
		if ts > lastFired[k] {
			lastFired[k] = ts
		}
	}

	for entity, idxs := range entityIndex {
		if len(idxs) > 40 {
			// Skip overly broad anchors so association graph remains sparse and selective.
			continue
		}
		baseW := 0.35
		if len(entity) > 7 && entity[:7] == "person:" {
			baseW = 0.25
		}
		for i := range len(idxs) {
			for j := i + 1; j < len(idxs); j++ {
				mi, mj := memories[idxs[i]], memories[idxs[j]]
				if mi.IsDistractor && mj.IsDistractor {
					continue
				}
				w := baseW
				if !mi.IsDistractor && !mj.IsDistractor {
					w += 0.15
				}
				dt := math.Abs(float64(mi.TEvent - mj.TEvent))
				if dt <= 2*3600 {
					w += 0.25
				}
				addWeight(idxs[i], idxs[j], w)
			}
		}
	}

	edges := make([]AssocEdge, 0, len(weights))
	for k, w := range weights {
		if w > 1.0 {
			w = 1.0
		}
		if w < 0.2 {
			continue
		}
		edges = append(edges, AssocEdge{
			A:         k.a,
			B:         k.b,
			Weight:    w,
			LastFired: lastFired[k],
		})
	}
	slices.SortFunc(edges, func(x, y AssocEdge) int {
		return cmp.Or(cmp.Compare(x.A, y.A), cmp.Compare(x.B, y.B))
	})
	return edges
}
