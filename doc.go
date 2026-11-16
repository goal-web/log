// Package log 是 Goal-Web 的日志组件，基于 zap 提供多通道、可级别过滤的结构化日志。
//
// 核心能力：contracts.Logger/ChannelLogger 的 zap 适配（zap.go）、按通道配置
// 构建 logger 的工厂与 stack 组合通道（factory.go）、通道解析/缓存/singleflight
// 与运行时注册自定义驱动的扩展点（manager.go）。
//
// Package log is the Goal-Web logging component, built on zap to provide
// multi-channel, level-filtered structured logging.
//
// Core capabilities: the zap adapter for contracts.Logger/ChannelLogger
// (zap.go), the factory building loggers from channel configurations together
// with stack composition channels (factory.go), and channel resolution with
// caching, singleflight and the runtime custom driver extension point
// (manager.go).
package log
