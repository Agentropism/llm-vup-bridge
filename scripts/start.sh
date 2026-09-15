#!/usr/bin/env bash
set -euo pipefail
bridge_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mode="${1:---all}"
if [[ "$mode" != "--all" && "$mode" != "--debug" ]]; then
  echo "用法: bash scripts/start.sh [--all|--debug]" >&2
  echo "可设置 LLM_VUP_ROOT、BRIDGE_CONFIG。--debug 只启动 Web 调试台。" >&2
  exit 2
fi
command -v go >/dev/null || { echo "请先安装 Go 1.26.4 或更新版本" >&2; exit 1; }
cd "$bridge_dir"
config_path="${BRIDGE_CONFIG:-$bridge_dir/config.json}"
if [[ "$mode" == "--all" ]]; then
  export LLM_VUP_ROOT="${LLM_VUP_ROOT:-$bridge_dir/../LLM-Vup}"
  [[ -f "$LLM_VUP_ROOT/run_server.py" ]] || { echo "找不到 LLM-Vup，请设置 LLM_VUP_ROOT" >&2; exit 1; }
  command -v uv >/dev/null || { echo "请先安装 uv，并在 LLM-Vup 中运行 uv sync" >&2; exit 1; }
fi
if [[ ! -f "$config_path" ]]; then
  if [[ -n "${BRIDGE_CONFIG:-}" ]]; then echo "配置不存在: $config_path" >&2; exit 1; fi
  (umask 077; cp "$bridge_dir/config.example.json" "$config_path")
fi
CGO_ENABLED=0 go build -o "$bridge_dir/distillery" ./cmd/distillery
if [[ "$mode" == "--debug" ]]; then exec "$bridge_dir/distillery" --config "$config_path"; fi
pids=()
cleanup() { for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done; wait || true; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
uv run --project "$LLM_VUP_ROOT" python "$bridge_dir/bridge/serve.py" &
pids+=("$!")
"$bridge_dir/distillery" --config "$config_path" &
pids+=("$!")
echo "调试台默认地址：http://localhost:9528/debug；在角色与输出设置中选择主项目，并填写主项目地址。"
wait -n "${pids[@]}"
