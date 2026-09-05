import sys, json, collections, os
ROOT = "/home/florent/kepler/repositories/.worktrees/sdk-jaimerais-que-tu-mexplique-dans-5b7bcb73/"
d = json.load(sys.stdin)
res = d.get("results") or []
mode = sys.argv[1] if len(sys.argv) > 1 else "pkg"
rows = []
for r in res:
    loc = r.get("location") or {}
    f = (loc.get("file") or "").replace(ROOT, "")
    rows.append((f, loc.get("line"), r.get("ruleId"), r.get("message")))
print("TOTAL", len(rows))
if mode == "pkg":
    c = collections.Counter(os.path.dirname(f) for f, _, _, _ in rows)
    for k, v in c.most_common(int(sys.argv[2]) if len(sys.argv) > 2 else 30):
        print("%5d  %s" % (v, k))
elif mode == "rule":
    c = collections.Counter(r for _, _, r, _ in rows)
    for k, v in c.most_common(60):
        print("%5d  %s" % (v, k))
else:
    for f, l, r, m in sorted(rows):
        print("%s:%s  %-22s %s" % (f, l, r, m))
