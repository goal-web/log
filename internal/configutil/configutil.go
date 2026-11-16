// Package configutil 提供 log 组件与 drivers 子包共享的配置读取工具，
// 消除 log 与 drivers 各自维护一份 configString 的复刻。
//
// 依赖方向：log → configutil ← drivers（internal 对同一模块内全部包可见）。
//
// Package configutil provides config-reading helpers shared by the log
// component and its drivers subpackage, removing the duplicated configString
// copies each previously maintained.
//
// Dependency direction: log -> configutil <- drivers (internal is visible to
// every package within the same module).
package configutil

import (
	"fmt"

	"github.com/goal-web/contracts"
)

// String 读取字符串配置项：字段缺失或值为 nil 时返回空串（由调用方应用默认值
// 或执行必填校验），字段存在但类型不是 string 时返回 error（与非法字符串值的
// 严格策略保持一致）。
// String reads a string config entry: a missing field or nil value yields ""
// (the caller applies its default or performs the required check), while a
// present non-string value returns an error, consistent with the strict policy
// for invalid string values.
func String(cfg contracts.Fields, key string) (string, error) {
	value, ok := cfg[key]
	if !ok || value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("log: %s 字段需为字符串，实际类型 %T %s field must be a string, got %T", key, value, key, value)
	}

	return str, nil
}
