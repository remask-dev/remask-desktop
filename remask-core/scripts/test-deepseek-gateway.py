#!/usr/bin/env python3
"""Black-box E2E checks for a running Remask OpenAI-compatible gateway."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import uuid
from dataclasses import dataclass
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


MASK_RE = re.compile(r"<MASK_EMAIL:[A-F0-9]{4}>")
CACHE_HIT_KEYS = {
    "cached_tokens",
    "cache_read_input_tokens",
    "cachedContentTokenCount",
    "prompt_cache_hit_tokens",
}
CACHE_MISS_KEYS = {"prompt_cache_miss_tokens"}


class CheckFailed(RuntimeError):
    pass


@dataclass
class HTTPResult:
    status: int
    headers: dict[str, str]
    body: Any


def http_json(
    method: str,
    url: str,
    *,
    payload: Any | None = None,
    headers: dict[str, str] | None = None,
    user_agent: str = "remask-deepseek-gateway-e2e/1.0",
    timeout: float = 120,
) -> HTTPResult:
    request_headers = {
        "Accept": "application/json",
        "User-Agent": user_agent,
        **(headers or {}),
    }
    data = None
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request_headers["Content-Type"] = "application/json"
    request = Request(url, data=data, headers=request_headers, method=method)
    try:
        with urlopen(request, timeout=timeout) as response:
            raw = response.read()
            return HTTPResult(
                response.status,
                dict(response.headers.items()),
                decode_json(raw, url),
            )
    except HTTPError as error:
        raw = error.read()
        detail = raw.decode("utf-8", errors="replace")
        raise CheckFailed(f"HTTP {error.code} {url}: {detail}") from error
    except (URLError, TimeoutError) as error:
        raise CheckFailed(f"无法访问 {url}: {error}") from error


def decode_json(raw: bytes, url: str) -> Any:
    try:
        return json.loads(raw)
    except json.JSONDecodeError as error:
        preview = raw[:500].decode("utf-8", errors="replace")
        raise CheckFailed(f"{url} 未返回合法 JSON: {preview}") from error


def require(condition: bool, message: str) -> None:
    if not condition:
        raise CheckFailed(message)


def join_url(base: str, path: str) -> str:
    return base.rstrip("/") + "/" + path.lstrip("/")


def nested_numbers(value: Any, keys: set[str]) -> list[int]:
    found: list[int] = []
    if isinstance(value, dict):
        for key, child in value.items():
            if key in keys and isinstance(child, (int, float)) and not isinstance(child, bool):
                found.append(int(child))
            found.extend(nested_numbers(child, keys))
    elif isinstance(value, list):
        for child in value:
            found.extend(nested_numbers(child, keys))
    return found


def response_text(body: Any) -> str:
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as error:
        raise CheckFailed("响应缺少 choices[0].message.content") from error
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            item.get("text", "") for item in content if isinstance(item, dict)
        )
    raise CheckFailed("choices[0].message.content 不是文本")


@dataclass
class CacheUsage:
    hit: int
    miss: int | None
    input_tokens: int


def cache_usage(body: Any, turn: str) -> CacheUsage:
    usage = body.get("usage") if isinstance(body, dict) else None
    require(isinstance(usage, dict), f"{turn}: 响应未提供 usage，无法验证缓存")
    hits = nested_numbers(usage, CACHE_HIT_KEYS)
    require(bool(hits), f"{turn}: usage 未提供缓存命中字段，无法验证缓存")
    misses = nested_numbers(usage, CACHE_MISS_KEYS)
    prompt_tokens = usage.get("prompt_tokens", usage.get("input_tokens", 0))
    require(
        isinstance(prompt_tokens, (int, float)) and prompt_tokens > 0,
        f"{turn}: 缺少有效的输入 token 数",
    )
    return CacheUsage(
        hit=max(hits),
        miss=max(misses) if misses else None,
        input_tokens=int(prompt_tokens),
    )


def check_initial_cache_miss(body: Any) -> CacheUsage:
    usage = cache_usage(body, "第一轮")
    require(usage.hit == 0, f"第一轮: 预期缓存未命中，实际命中 token={usage.hit}")
    if usage.miss is not None:
        require(usage.miss > 0, "第一轮: prompt_cache_miss_tokens 应大于 0")
    return usage


def find_audit(
    management_url: str,
    marker: str,
    *,
    user_agent: str,
    timeout: float,
    wait_seconds: float = 5,
) -> dict[str, Any]:
    query = urlencode({"limit": "20", "search": marker})
    deadline = time.monotonic() + wait_seconds
    while True:
        result = http_json(
            "GET",
            join_url(management_url, f"/api/v1/audit/logs?{query}"),
            user_agent=user_agent,
            timeout=timeout,
        )
        logs = result.body.get("logs", []) if isinstance(result.body, dict) else []
        for entry in logs:
            if isinstance(entry, dict) and any(
                marker in str(field.get("redacted", ""))
                for field in entry.get("fields", [])
                if isinstance(field, dict)
            ):
                return entry
        if time.monotonic() >= deadline:
            raise CheckFailed(f"审计日志中未找到请求标识 {marker}")
        time.sleep(0.2)


def field_map(entry: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {
        field["path"]: field
        for field in entry.get("fields", [])
        if isinstance(field, dict) and isinstance(field.get("path"), str)
    }


def validate_audit_common(
    entry: dict[str, Any], turn: str, expected_cached: int
) -> None:
    require(entry.get("status_code") == 200, f"{turn}: 审计状态码不是 200")
    require(entry.get("operation_id") == "create-chat-completion", f"{turn}: 未命中聊天适配方案")
    require(entry.get("protection_mode") == "redacted", f"{turn}: 请求未处于脱敏保护模式")
    cached = entry.get("token_usage", {}).get("cached")
    require(
        cached == expected_cached,
        f"{turn}: 响应缓存命中 token={expected_cached}，审计记录为 {cached}",
    )


def long_cache_prefix(run_id: str, minimum_chars: int) -> str:
    seed = (
        "缓存前缀稳定性测试：请保留此段上下文，用于验证多轮请求的公共前缀缓存。"
        "除每轮最后一条用户消息外，前面的系统消息和历史消息必须保持完全一致。"
    )
    filler = (seed + "\n") * ((minimum_chars // (len(seed) + 1)) + 1)
    return (
        f"REMASK_CACHE_RUN_{run_id}\n"
        f"{filler[:minimum_chars]}\n"
        "这是自动化测试。请严格按最后一条用户消息指定的格式回答，不要解释。"
    )


def validate_user_field(
    fields: dict[str, dict[str, Any]], path: str, secret: str, turn: str
) -> None:
    require(path in fields, f"{turn}: 审计缺少用户字段 {path}")
    redacted = str(fields[path].get("redacted", ""))
    require(secret not in redacted, f"{turn}: 用户敏感内容被原样发送给上游")
    require(bool(MASK_RE.search(redacted)), f"{turn}: 用户邮箱没有被替换为 MASK_EMAIL 标签")
    entities = fields[path].get("entities", [])
    require(
        any(entity.get("type") == "EMAIL" for entity in entities if isinstance(entity, dict)),
        f"{turn}: 审计未记录 EMAIL 实体",
    )


def preflight(management_url: str, timeout: float, user_agent: str) -> None:
    health = http_json(
        "GET",
        join_url(management_url, "/api/v1/health"),
        timeout=timeout,
        user_agent=user_agent,
    )
    require(health.body.get("status") == "ok", "管理端健康检查失败")

    policy = http_json(
        "GET",
        join_url(management_url, "/api/v1/policy"),
        timeout=timeout,
        user_agent=user_agent,
    ).body
    require(policy.get("enabled") is True, "全局脱敏策略未启用")
    require(
        policy.get("redact_ai_answers") is False,
        "redact_ai_answers 必须为 false，才能验证 AI 回答内容不脱敏",
    )
    email_rules = [
        rule
        for rule in policy.get("rules", [])
        if isinstance(rule, dict) and str(rule.get("id", "")).upper() == "EMAIL"
    ]
    require(any(rule.get("enabled") is True for rule in email_rules), "EMAIL 脱敏规则未启用")

    settings = http_json(
        "GET",
        join_url(management_url, "/api/v1/settings"),
        timeout=timeout,
        user_agent=user_agent,
    ).body
    require(
        settings.get("audit", {}).get("record_request_logs") is True,
        "请求日志未启用，无法验证实际发送给上游的内容",
    )


def chat(
    gateway_url: str,
    messages: list[dict[str, str]],
    *,
    model: str,
    api_key: str,
    user_agent: str,
    timeout: float,
) -> Any:
    headers: dict[str, str] = {}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    result = http_json(
        "POST",
        join_url(gateway_url, "/v1/chat/completions"),
        payload={"model": model, "messages": messages, "stream": False},
        headers=headers,
        user_agent=user_agent,
        timeout=timeout,
    )
    require(result.status == 200, f"聊天请求返回 HTTP {result.status}")
    return result.body


def run(args: argparse.Namespace) -> None:
    preflight(args.management_url, args.timeout, args.user_agent)
    print("PASS 预检：网关健康，EMAIL 规则及审计已启用，AI 回答脱敏已关闭")

    run_id = uuid.uuid4().hex[:12]
    marker1 = f"REMASK_E2E_T1_{run_id}"
    marker2 = f"REMASK_E2E_T2_{run_id}"
    marker3 = f"REMASK_E2E_T3_{run_id}"
    email1 = f"turn1-{run_id}@example.test"
    email2 = f"turn2-{run_id}@example.test"
    email3 = f"turn3-{run_id}@example.test"
    ai_email_local = f"ai-{run_id}"
    ai_email = f"{ai_email_local}@example.test"

    turn1_messages = [
        {
            "role": "system",
            "content": long_cache_prefix(run_id, args.cache_prefix_chars),
        },
        {
            "role": "user",
            "content": (
                f"{marker1}。我的邮箱是 {email1}。请只回复一行：先写我的邮箱，"
                f"再把片段 {ai_email_local} 和 example.test 用电子邮件分隔符拼成另一个邮箱。"
            ),
        },
    ]
    turn1_body = chat(
        args.gateway_url,
        turn1_messages,
        model=args.model,
        api_key=args.api_key,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    turn1_answer = response_text(turn1_body)
    require(email1 in turn1_answer, "第一轮 AI 回答未包含还原后的测试邮箱")
    require(ai_email in turn1_answer, "第一轮 AI 回答未生成预期的敏感邮箱内容")
    require(not MASK_RE.search(turn1_answer), "第一轮 AI 回答仍包含脱敏标签")
    cache1 = check_initial_cache_miss(turn1_body)

    audit1 = find_audit(
        args.management_url,
        marker1,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    validate_audit_common(audit1, "第一轮", cache1.hit)
    fields1 = field_map(audit1)
    validate_user_field(fields1, "/messages/1/content", email1, "第一轮")
    print(
        f"PASS 第一轮（预热）：input={cache1.input_tokens}, "
        f"cache_hit={cache1.hit}, cache_miss={cache1.miss}"
    )

    if args.cache_warmup_wait > 0:
        time.sleep(args.cache_warmup_wait)

    turn2_messages = turn1_messages + [
        {"role": "assistant", "content": turn1_answer},
        {
            "role": "user",
            "content": (
                f"{marker2}。新邮箱是 {email2}。请只回复上一轮回答里的两个邮箱，"
                "再回复这个新邮箱。"
            ),
        },
    ]
    turn2_body = chat(
        args.gateway_url,
        turn2_messages,
        model=args.model,
        api_key=args.api_key,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    turn2_answer = response_text(turn2_body)
    require(email1 in turn2_answer, "第二轮 AI 回答未保留上一轮的测试邮箱")
    require(ai_email in turn2_answer, "第二轮 AI 回答未保留上一轮由 AI 生成的敏感邮箱")
    require(email2 in turn2_answer, "第二轮 AI 回答未包含本轮还原后的测试邮箱")
    require(not MASK_RE.search(turn2_answer), "第二轮 AI 回答仍包含脱敏标签")
    cache2 = cache_usage(turn2_body, "第二轮")

    audit2 = find_audit(
        args.management_url,
        marker2,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    validate_audit_common(audit2, "第二轮", cache2.hit)
    fields2 = field_map(audit2)
    validate_user_field(fields2, "/messages/1/content", email1, "第二轮历史用户消息")
    validate_user_field(fields2, "/messages/3/content", email2, "第二轮新用户消息")
    require(
        "/messages/2/content" not in fields2,
        "第二轮 assistant 历史被纳入脱敏字段，AI 回答内容不应脱敏",
    )
    print(
        f"PASS 第二轮（复用首轮前缀）：input={cache2.input_tokens}, "
        f"cache_hit={cache2.hit}, cache_miss={cache2.miss}"
    )

    if args.cache_warmup_wait > 0:
        time.sleep(args.cache_warmup_wait)

    turn3_messages = turn2_messages + [
        {"role": "assistant", "content": turn2_answer},
        {
            "role": "user",
            "content": (
                f"{marker3}。第三个邮箱是 {email3}。请只回复本轮对话出现的所有邮箱。"
            ),
        },
    ]
    turn3_body = chat(
        args.gateway_url,
        turn3_messages,
        model=args.model,
        api_key=args.api_key,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    turn3_answer = response_text(turn3_body)
    for expected in (email1, ai_email, email2, email3):
        require(expected in turn3_answer, f"第三轮 AI 回答缺少邮箱 {expected}")
    require(not MASK_RE.search(turn3_answer), "第三轮 AI 回答仍包含脱敏标签")
    cache3 = cache_usage(turn3_body, "第三轮")

    audit3 = find_audit(
        args.management_url,
        marker3,
        user_agent=args.user_agent,
        timeout=args.timeout,
    )
    validate_audit_common(audit3, "第三轮", cache3.hit)
    fields3 = field_map(audit3)
    validate_user_field(fields3, "/messages/1/content", email1, "第三轮历史用户消息一")
    validate_user_field(fields3, "/messages/3/content", email2, "第三轮历史用户消息二")
    validate_user_field(fields3, "/messages/5/content", email3, "第三轮新用户消息")
    require(
        "/messages/2/content" not in fields3 and "/messages/4/content" not in fields3,
        "第三轮 assistant 历史被纳入脱敏字段",
    )
    print(
        f"PASS 第三轮（复用前两轮前缀）：input={cache3.input_tokens}, "
        f"cache_hit={cache3.hit}, cache_miss={cache3.miss}"
    )

    require(
        max(cache2.hit, cache3.hit) > 0,
        "第二、三轮缓存命中 token 均为 0；上游缓存未命中或未启用",
    )
    print(
        f"PASS 缓存命中验证通过：warmup=0, turn2={cache2.hit}, "
        f"turn3={cache3.hit}（model={args.model}, run_id={run_id}）"
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="测试 Remask 网关的脱敏、多轮对话以及公共前缀缓存命中行为"
    )
    parser.add_argument(
        "--gateway-url",
        default=os.getenv("REMASK_GATEWAY_URL", "http://127.0.0.1:17681"),
        help="AI 代理地址（默认：http://127.0.0.1:17681）",
    )
    parser.add_argument(
        "--management-url",
        default=os.getenv("REMASK_MANAGEMENT_URL", "http://127.0.0.1:17680"),
        help="管理及审计 API 地址（默认：http://127.0.0.1:17680）",
    )
    parser.add_argument(
        "--model",
        default=os.getenv("REMASK_TEST_MODEL", "deepseek-v4-flash"),
        help="模型名称（默认：deepseek-v4-flash）",
    )
    parser.add_argument(
        "--api-key",
        default=os.getenv("REMASK_TEST_API_KEY", ""),
        help="上游凭证透传；也可使用 REMASK_TEST_API_KEY（托管凭证模式无需设置）",
    )
    parser.add_argument(
        "--user-agent",
        default=os.getenv(
            "REMASK_TEST_USER_AGENT", "remask-deepseek-gateway-e2e/1.0"
        ),
        help="请求 User-Agent；也可使用 REMASK_TEST_USER_AGENT",
    )
    parser.add_argument(
        "--cache-prefix-chars",
        type=int,
        default=int(os.getenv("REMASK_TEST_CACHE_PREFIX_CHARS", "6000")),
        help="首轮公共缓存前缀字符数（默认：6000）",
    )
    parser.add_argument(
        "--cache-warmup-wait",
        type=float,
        default=float(os.getenv("REMASK_TEST_CACHE_WARMUP_WAIT", "1")),
        help="每轮后等待缓存可见的秒数（默认：1）",
    )
    parser.add_argument("--timeout", type=float, default=120, help="单次 HTTP 超时秒数")
    args = parser.parse_args()
    parser.error("--cache-prefix-chars 必须至少为 1024") if args.cache_prefix_chars < 1024 else None
    parser.error("--cache-warmup-wait 不能为负数") if args.cache_warmup_wait < 0 else None
    return args


def main() -> int:
    try:
        run(parse_args())
        return 0
    except CheckFailed as error:
        print(f"FAIL {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("FAIL 测试被中断", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
