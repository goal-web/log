// Package log 是 Goal-Web 的日志组件，基于 zap 实现多通道日志。
//
// 组件实现完善前，此文件仅用于保持模块可编译并固定依赖。
package log

import (
	_ "github.com/goal-web/contracts"
	_ "go.uber.org/zap"
)
