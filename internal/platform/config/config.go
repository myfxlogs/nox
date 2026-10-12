// Package config 集中管理全链路经济参数（D-035：禁止散落硬编码魔法数字）。
// 全部 env 可覆盖（ARB_* 前缀），默认值单一出处；阈值初值取保守默认，
// 由 D-034 统计复盘以真实成交数据校准。
package config

// Load 从环境变量加载配置——已提取至 config_load.go（D-261 S6 纯搬家拆分）。
