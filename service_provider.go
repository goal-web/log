// service_provider.go：日志组件的服务提供者——把 LogManager 注册为容器的
// "log" 单例（config.Get("log") 配置节 → ParseConfig 解析 → NewFactory 兜底
// 构建），业务侧经依赖注入拿到 contracts.LogManager。
//
// 注意：supports/logs 的 SetDefault 接线不在组件内做——log 一旦 import
// supports，与 supports 改代理后的 supports/logs → log 形成模块循环依赖；
// 该接线由 goal 骨架在服务注册后完成。
//
// service_provider.go: the log component's service provider — registers the
// LogManager as the container's "log" singleton (the config.Get("log") section
// -> ParseConfig -> NewFactory with zero-config fallback), from which the
// application obtains contracts.LogManager via dependency injection.
//
// Note: the supports/logs SetDefault wiring is deliberately NOT done here —
// once log imports supports, it forms a module dependency cycle with
// supports/logs -> log after supports becomes the proxy; the goal skeleton
// performs that wiring after service registration instead.
package log

import (
	"github.com/goal-web/contracts"
)

// serviceProvider 实现 log 服务的 contracts.ServiceProvider 接口。
// serviceProvider implements the contracts.ServiceProvider interface for the
// log service.
type serviceProvider struct {
}

// NewService 创建日志服务提供者的新实例。
// NewService creates a new instance of the log service provider.
func NewService() contracts.ServiceProvider {
	return serviceProvider{}
}

// Stop 停止日志服务提供者。
// Stop stops the log service provider.
func (provider serviceProvider) Stop() {

}

// Start 启动日志服务提供者。
// Start starts the log service provider.
func (provider serviceProvider) Start() error {
	return nil
}

// Register 在容器中注册 "log" 单例：配置节缺失或形态非法时 ParseConfig 交给
// NewFactory 兜底，保证开箱即用；单例可在构建后经 Extend 注册自定义驱动。
//
// config.Get("log") 的期望形态为 log.Config（强类型直通）或 contracts.Fields
// （与 TOML [log] 节同构），其余形态走零配置兜底。注意 config 组件把 env.toml
// 压平为点分键存储，容器里的 "log" 键必须由 goal 骨架的 ConfigProvider 注册
// （参照 goal/config/cache.go 的 init() 模式）；只在 env.toml 写 [log] 节而未
// 注册 provider 时 config.Get("log") 为 nil，静默走零配置兜底。
//
// Register registers the "log" singleton in the container: a missing or
// malformed config section is left to NewFactory's fallback through
// ParseConfig, so the component works out of the box; the singleton accepts
// custom drivers via Extend after construction.
//
// config.Get("log") is expected to be a log.Config (strongly-typed
// passthrough) or contracts.Fields (mirroring the TOML [log] section); any
// other shape takes the zero-config fallback. Note that the config component
// flattens env.toml into dot-separated keys, so the container's "log" key must
// be registered by the goal skeleton's ConfigProvider (cf. the init() pattern
// in goal/config/cache.go); writing only a [log] section in env.toml without a
// provider leaves config.Get("log") nil and silently takes the zero-config
// fallback.
func (provider serviceProvider) Register(container contracts.Application) {
	container.Singleton("log", func(config contracts.Config) contracts.LogManager {
		return NewFactory(ParseConfig(config.Get("log")))
	})
}
