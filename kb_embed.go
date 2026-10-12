// Package kb 经验库 embed（F8：只读浏览页，容器内无 docs/ 目录）。
// embed 路径相对此 .go 文件（项目根），embed docs/经验库/ 全集。
// import path = "arb"（module arb 根包）；package 名 kb 与 import path 解耦。
package kb

import "embed"

//go:embed docs/经验库/索引.md
var IndexMarkdown string

//go:embed docs/经验库/条目
var EntriesDir embed.FS
