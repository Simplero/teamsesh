# usage: claude_strip.py src.jsonl dst.jsonl new_session_id new_cwd
# Keeps only user and assistant turns, relinks parentUuid past dropped records.
import json, sys
src, dst, new, cwd = sys.argv[1:]
recs = [json.loads(l) for l in open(src)]
parent = {r['uuid']: r.get('parentUuid') for r in recs if 'uuid' in r}
keep = [r for r in recs if r['type'] in ('user', 'assistant')]
kept = {r['uuid'] for r in keep}
with open(dst, 'w') as out:
    for r in keep:
        p = r.get('parentUuid')
        while p and p not in kept:
            p = parent.get(p)
        r['parentUuid'] = p
        r['sessionId'] = new
        r['cwd'] = cwd
        out.write(json.dumps(r) + '\n')
