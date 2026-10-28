# usage: codex_strip.py src.jsonl dst.jsonl new_session_id new_cwd
# Keeps session_meta (minus instructions and account IDs) and real user/assistant messages.
import json,sys
src,dst,new,cwd=sys.argv[1:]
out=open(dst,'w')
for l in open(src):
    d=json.loads(l); t=d['type']; p=d.get('payload',{})
    if t=='session_meta':
        for k in ('base_instructions','creator_account_id','creator_user_id','runtime_workspace_roots'): p.pop(k,None)
        p['id']=new; p['session_id']=new; p['cwd']=cwd
    elif t=='response_item' and p.get('type')=='message' and p.get('role') in ('user','assistant'):
        txt=''.join(c.get('text','') for c in p.get('content',[]))
        if p['role']=='user' and txt.lstrip().startswith(('<','# AGENTS.md')): continue
    else:
        continue
    out.write(json.dumps(d)+'\n')
