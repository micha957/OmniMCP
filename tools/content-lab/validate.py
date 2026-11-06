#!/usr/bin/env python3
"""Tests each content rule: does doing it beat not doing it, in real data?

Usage: validate.py docs/content-data/<samples>.json

Each rule is a yes/no feature. Notes are compared within the same search keyword
(a stratified permutation test on the median difference of log counts), then
corrected for multiple comparisons. Results are correlations, not causes: follower
counts are unknown. Our own posts in the playbook are the stronger evidence.
"""
import json
import math
import random
import re
import sys
from collections import defaultdict
from statistics import median

MIN_GROUP = 8
MIN_RATIO = 1.5
ROUNDS = 5000


def has_visual(*kinds):
    return lambda n: n.get("cover") in kinds if n.get("cover") else None


def flag(name):
    return lambda n: n[name] if name in n else None


def title_match(pattern):
    return lambda n: bool(re.search(pattern, n["title"])) if n["title"] else None


def body_feature(name, test):
    return lambda n: test(n["body"][name]) if "body" in n else None


RULES = {
    "封面：实拍成品": has_visual("finished"),
    "封面：手持入镜": flag("hand"),
    "封面：大字标题": flag("big_text"),
    "封面：纯文字卡": has_visual("text_card"),
    "封面：信息图": has_visual("info"),
    "封面：原图与结果对比": has_visual("compare"),
    "封面：图纸网格": has_visual("pattern"),
    "封面：插画或生成图": has_visual("illustration"),
    "封面：操作过程": has_visual("process"),
    "封面：软件截图": has_visual("screenshot"),
    "标题：第一人称": title_match(r"我|自己"),
    "标题：问句": title_match(r"[？?]|怎么|如何"),
    "标题：有数字": title_match(r"\d"),
    "标题：有 emoji": title_match(r"[\U0001F300-\U0001FAFF☀-➿]"),
    "标题：保姆级、新手必看等套话": title_match(r"保姆级|必看|抄作业|零失败|全流程"),
    "标题：附图纸": title_match(r"图纸"),
    "正文：编号列表": body_feature("numbered", lambda v: v >= 2),
    "正文：第一人称": body_feature("first_person", lambda v: v >= 1),
    "正文：口语情绪词": body_feature("slang", lambda v: v >= 1),
    "正文：含问句": body_feature("questions", lambda v: v >= 1),
    "正文：短于 100 字": body_feature("chars", lambda v: v < 100),
}

METRICS = {
    "赞": lambda n: math.log1p(n["likes"]),
    "藏": lambda n: math.log1p(n["collects"]),
    "评": lambda n: math.log1p(n["comments"]),
    "评/赞": lambda n: math.log1p(n["comments"]) - math.log1p(n["likes"]),
    "藏/赞": lambda n: math.log1p(n["collects"]) - math.log1p(n["likes"]),
}


def permutation_p(rows, rounds):
    """Two-sided p for the median difference, shuffling labels inside each keyword."""
    def diff(labels):
        yes = [v for (v, _), l in zip(rows, labels) if l]
        no = [v for (v, _), l in zip(rows, labels) if not l]
        return median(yes) - median(no)

    observed = abs(diff([l for _, l in rows_labels(rows)]))
    strata = defaultdict(list)
    for i, row in enumerate(rows):
        strata[row[1][0]].append(i)
    labels = [r[1][1] for r in rows]
    hits = 0
    for _ in range(rounds):
        shuffled = labels[:]
        for idx in strata.values():
            values = [labels[i] for i in idx]
            random.shuffle(values)
            for i, v in zip(idx, values):
                shuffled[i] = v
        if abs(diff(shuffled)) >= observed:
            hits += 1
    return (hits + 1) / (rounds + 1)


def rows_labels(rows):
    return [(r[0], r[1][1]) for r in rows]


def evaluate(notes):
    results = []
    for rule, feature in RULES.items():
        marked = [(n, feature(n)) for n in notes]
        marked = [(n, v) for n, v in marked if v is not None]
        yes = sum(1 for _, v in marked if v)
        no = len(marked) - yes
        for metric, value in METRICS.items():
            result = {"rule": rule, "metric": metric, "yes": yes, "no": no}
            if yes >= MIN_GROUP and no >= MIN_GROUP:
                rows = [(value(n), (n.get("query", ""), bool(v))) for n, v in marked]
                a = [r[0] for r in rows if r[1][1]]
                b = [r[0] for r in rows if not r[1][1]]
                result["ratio"] = math.exp(median(a) - median(b))
                result["p"] = permutation_p(rows, ROUNDS)
            results.append(result)
    return results


def correct(results):
    """Benjamini-Hochberg over every comparison actually tested."""
    tested = sorted((r for r in results if "p" in r), key=lambda r: r["p"])
    m = len(tested)
    smallest = 1.0
    for rank, r in reversed(list(enumerate(tested, 1))):
        smallest = min(smallest, r["p"] * m / rank)
        r["q"] = smallest


def verdict(r):
    if "p" not in r:
        return "样本不足"
    strong = r["ratio"] >= MIN_RATIO or r["ratio"] <= 1 / MIN_RATIO
    if r["q"] < 0.1 and strong:
        return "明显好" if r["ratio"] > 1 else "明显差"
    if r["p"] < 0.05 and strong:
        return "有迹象（好）" if r["ratio"] > 1 else "有迹象（差）"
    return "没有明显差异"


def main():
    random.seed(20261106)
    data = json.load(open(sys.argv[1]))
    notes = [n for n in data["notes"] if n.get("type") != "video"]
    results = evaluate(notes)
    correct(results)
    print(f"图文笔记 {len(notes)} 篇；比值为做了 ÷ 没做的中位数\n")
    print("规则 | 指标 | 做了/没做 | 比值 | p | q | 结论")
    order = {"明显好": 0, "明显差": 1, "有迹象（好）": 2, "有迹象（差）": 3, "没有明显差异": 4, "样本不足": 5}
    out = []
    for r in results:
        r["verdict"] = verdict(r)
        out.append(r)
    for r in sorted(out, key=lambda r: (order[r["verdict"]], r["rule"], r["metric"])):
        if r["verdict"] == "没有明显差异" and "--all" not in sys.argv:
            continue
        ratio = f'{r["ratio"]:.2f}' if "ratio" in r else "-"
        p = f'{r["p"]:.3f}' if "p" in r else "-"
        q = f'{r["q"]:.3f}' if "q" in r else "-"
        print(f'{r["rule"]} | {r["metric"]} | {r["yes"]}/{r["no"]} | {ratio} | {p} | {q} | {r["verdict"]}')
    tested = sum(1 for r in out if "p" in r)
    flat = sum(1 for r in out if r["verdict"] == "没有明显差异")
    print(f"\n共检验 {tested} 组，其中 {flat} 组没有明显差异（未列出）")


if __name__ == "__main__":
    main()
