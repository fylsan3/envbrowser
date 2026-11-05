// Package templates 内置 macOS 模板（presets.json），编译时打包进程序。
package templates

import _ "embed"

//go:embed presets.json
var Presets []byte
