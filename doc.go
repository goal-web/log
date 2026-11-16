// Package log 是 Goal-Web 的日志组件，基于 zap 提供多通道、可级别过滤的结构化日志。
//
// 核心能力：contracts.Logger/ChannelLogger 的 zap 适配（zap.go）、按通道配置
// 构建 logger 的工厂（factory.go）、以及运行时注册自定义驱动的扩展点。
//
// Package log is the Goal-Web logging component, built on zap to provide
// multi-channel, level-filtered structured logging.
//
// Core capabilities: the zap adapter for contracts.Logger/ChannelLogger
// (zap.go), a factory that builds loggers from channel configurations
// (factory.go), and an extension point for registering custom drivers at
// runtime.
package log
