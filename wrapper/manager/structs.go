package main

type StatusResp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
	Data struct {
		Status      bool     `bson:"status"`
		Regions     []string `bson:"regions"`
		ClientCount int      `bson:"clientCount"`
	} `bson:"data"`
}

type LoginReq struct {
	Cmd  string `bson:"cmd"`
	Data struct {
		Username    string `bson:"username"`
		Password    string `bson:"password"`
		TwoStepCode int    `bson:"twoStepCode"`
	}
}

type LoginResp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
}

type DecryptReq struct {
	Cmd  string `bson:"cmd"`
	Data struct {
		AdamId      string `bson:"adamId"`
		Key         string `bson:"key"`
		SampleIndex int    `bson:"sampleIndex"`
		Sample      []byte `bson:"sample"`
	} `bson:"data"`
}

type DecryptResp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
	Data struct {
		AdamId      string `bson:"adamId"`
		Key         string `bson:"key"`
		SampleIndex int    `bson:"sampleIndex"`
		Sample      []byte `bson:"sample"`
	} `bson:"data"`
}

type M3U8Req struct {
	Cmd  string `bson:"cmd"`
	Data struct {
		AdamId string `bson:"adamId"`
	}
}

type M3U8Resp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
	Data struct {
		AdamId string `bson:"adamId"`
		M3U8   string `bson:"m3u8"`
	} `bson:"data"`
}

type LyricsReq struct {
	Cmd  string `bson:"cmd"`
	Data struct {
		AdamId   string `bson:"adamId"`
		Region   string `bson:"region"`
		Language string `bson:"language"`
	} `bson:"data"`
}

type LyricsResp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
	Data struct {
		AdamId string `bson:"adamId"`
		Lyrics string `bson:"lyrics"`
	} `bson:"data"`
}

type ErrorResp struct {
	Cmd  string `bson:"cmd"`
	Code int    `bson:"code"`
	Msg  string `bson:"msg"`
}
