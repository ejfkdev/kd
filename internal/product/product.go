// Package product defines the contract every dump target product implements.
// New platforms (wecom/wechat-mp/wechat-miniprogram/telegram/lanxin/...) get
// their own internal/<name> package and register one Spec here.
package product

// ModuleInfo describes one dump module (used by `kd list` and -only/-skip).
type ModuleInfo struct {
	ID    string // e.g. "im.chats"
	Group string
	Desc  string
}

// Spec describes a dump target product.
type Spec struct {
	Name  string // CLI 子命令与 -out 目录前缀, e.g. "feishu"
	Title string // 人类可读名, e.g. "飞书"

	// DetectCredential 判断一组松散凭据是否属于本产品（kd run 用）。
	DetectCredential func(appID, token string) bool

	// TranslateFlags 把 kd run 的通用凭据参数翻译成本产品自己的参数名。
	// 例如钉钉把 -token 译成 -access-token；多数产品可返回原参数。
	TranslateFlags func(argv []string) []string

	// Run 解析 argv 并执行本产品的完整 dump 流程（进程级，失败自行 os.Exit）。
	Run func(argv []string)

	// ListModules 返回本产品的全部数据模块（kd list 用）。
	ListModules func() []ModuleInfo
}
