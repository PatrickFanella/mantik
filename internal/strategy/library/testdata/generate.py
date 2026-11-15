"""Reference recipes executed through pinned upstream bt/ffn building blocks.

These are the explicitly adapted Mantik recipes, not untouched Lean algorithms
or a replication of the cited papers' empirical portfolios. Prices are real
retained inputs; fixture membership, clocks and zero-cost fills are synthetic.
"""
import hashlib
import importlib.metadata as metadata
import json
from decimal import Decimal, ROUND_DOWN, getcontext
from pathlib import Path
from types import SimpleNamespace

import bt
import ffn
import numpy as np
import pandas as pd

PINS = {'bt':'763ae01047fce2d09411faeba436855ecf8b679c','ffn':'eb9354c5c82dbada5645569dc34e5861d156e471'}
for package, revision in PINS.items():
    url = json.loads(metadata.distribution(package).read_text('direct_url.json'))
    assert url['vcs_info']['commit_id'] == revision
root = Path(__file__).parent
source = root/'adjusted-v1.csv'
assert hashlib.sha256(source.read_bytes()).hexdigest() == 'f6fa15c42a70121716541e962d1a5d27bd48f41d63bd980428702a69d5ccd391'
rows = pd.read_csv(source, parse_dates=['date'])
prices = rows.pivot(index='date',columns='asset',values='close').sort_index()
opens = rows.pivot(index='date',columns='asset',values='open').sort_index()
assets = list(prices.columns)
getcontext().prec=60
unit=Decimal('0.000000000001')
def decimal(text):return Decimal(str(text))
def trunc(value):return value.quantize(unit,rounding=ROUND_DOWN)

def reference(kind, count):
    periods={'trend':[21,63,252],'momentum':[63,126,252],'reversal':[5,21],'lowvol':[]}[kind]
    vol=252 if kind=='lowvol' else 63
    skip=21 if kind=='momentum' else 0
    warm=max([vol]+[p+skip for p in periods])+1
    old={};last=None;pending=None;cash=Decimal(100000);holdings={};decisions=[];fills=[]
    for i,date in enumerate(prices.index):
        if pending is not None:
            equity=cash+sum(holdings.get(a,Decimal(0))*decimal(opens.loc[date,a]) for a in assets)
            for a in assets:
                want=trunc(equity*decimal(pending.get(a,0))/decimal(opens.loc[date,a]))
                delta=want-holdings.get(a,Decimal(0))
                if delta:
                    fills.append(dict(Date=str(date.date()),Asset=a,Shares=str(delta)))
                    cash-=delta*decimal(opens.loc[date,a]);holdings[a]=want
            pending=None
        if i+1<warm:continue
        eligible=assets.copy()
        # Deliberate point-in-time membership changes exercise exits and re-entry.
        if kind!='trend' and 420<=i<480:eligible.remove('QQQ')
        key=date.strftime('%Y-%m')
        if kind=='reversal':key=f'{date.isocalendar().year:04d}-W{date.isocalendar().week:02d}'
        forced=any(w and a not in eligible for a,w in old.items())
        if key==last and not forced:
            decisions.append(dict(Date=str(date.date()),Hold=True,Targets={}))
            continue
        target=SimpleNamespace(now=date,universe=prices.iloc[:i+1],temp={'selected':eligible})
        risk=target.universe[eligible].iloc[-vol-1:].pct_change(fill_method=None).std(ddof=0)
        # Missing/invalid signal inputs cannot be restored by turnover smoothing.
        available = {a for a in eligible if np.isfinite(risk[a]) and risk[a] > 1e-12
                     and len(target.universe[a].iloc[-warm:]) == warm
                     and np.isfinite(target.universe[a].iloc[-warm:]).all()
                     and (target.universe[a].iloc[-warm:] > 0).all()}
        eligible = [a for a in eligible if a in available]
        target.temp['selected'] = eligible
        if kind=='lowvol':stat=-risk
        elif kind=='trend':
            signals=[]
            for period in periods:
                algo=bt.algos.StatTotalReturn(lookback=pd.DateOffset(days=(date-prices.index[i-period]).days))
                assert algo(target)
                signals.append(np.sign(target.temp['stat']))
            stat=pd.concat(signals,axis=1).mean(axis=1)
        else:
            endpoint=prices.index[i-skip]
            lookbacks=[pd.DateOffset(days=(endpoint-prices.index[i-skip-p]).days) for p in periods]
            algo=bt.algos.StatMultiPeriodReturn(lookbacks,lag=pd.DateOffset(days=(date-endpoint).days))
            assert algo(target)
            stat=target.temp['stat']*(-1 if kind=='reversal' else 1)
        target.temp['stat']=stat.loc[eligible]
        if kind!='trend':assert bt.algos.SelectN(count)(target)
        selected=target.temp['selected']
        if kind in ('trend','momentum'):
            assert bt.algos.WeighInvVol(lookback=vol+1)(target)
            weights=dict(target.temp['weights'])
            if kind=='trend':weights={a:weights[a]*stat[a] for a in selected}
        else:
            assert bt.algos.WeighEqually()(target)
            weights=dict(target.temp['weights'])
        denominator=sum(abs(weights[a]) for a in selected)
        weights={a:.7*weights[a]/denominator if denominator else 0 for a in selected}
        if kind=='reversal' and not forced:
            for a in old:weights.setdefault(a,0)
            change=sum(abs(weights[a]-old.get(a,0)) for a in sorted(weights) if a in available)
            if change>.25:
                weights={a:old.get(a,0)+.25/change*(w-old.get(a,0)) if a in available else 0 for a,w in weights.items()}
        weights={a:np.trunc(w*1e12)/1e12 for a,w in weights.items()}
        decisions.append(dict(Date=str(date.date()),Hold=False,Targets={a:f'{w:.12f}' for a,w in sorted(weights.items()) if w}))
        old=weights;last=key;pending=weights.copy()
    config = dict(kind=kind, lookbacks=periods or None, skip=skip, vol_window=vol,
                  count=count, cadence='weekly' if kind=='reversal' else 'monthly',
                  gross=.7, cash_rate=0, turnover=.25)
    config_sha = hashlib.sha256(json.dumps(config, separators=(',', ':')).encode()).hexdigest()
    return dict(Config=config, ConfigSHA256=config_sha, Decisions=decisions, Fills=fills)
cases = {kind+'_default':reference(kind,5) for kind in ['trend','momentum','reversal','lowvol']}
cases.update({kind+'_selection':reference(kind,2) for kind in ['momentum','reversal','lowvol']})
output = dict(Version='bt-763ae010-ffn-eb9354c5-recipes-v2',
              InputSHA256=hashlib.sha256(source.read_bytes()).hexdigest(), Pins=PINS, Cases=cases)
# Keep one decision/fill per line so reviewers can inspect numerical changes
# without a huge expanded fixture diff. This changes formatting, not values.
lines = ['{']
for key in ('Version', 'InputSHA256', 'Pins'):
    lines.append('  '+json.dumps(key)+': '+json.dumps(output[key], allow_nan=False)+',')
lines.append('  "Cases": {')
for case_index, (name, case) in enumerate(cases.items()):
    lines.append('    '+json.dumps(name)+': {')
    for key in ('Config', 'ConfigSHA256'):
        lines.append('      '+json.dumps(key)+': '+json.dumps(case[key], allow_nan=False)+',')
    for key in ('Decisions', 'Fills'):
        lines.append('      '+json.dumps(key)+': [')
        values = case[key]
        for index, value in enumerate(values):
            lines.append('        '+json.dumps(value, allow_nan=False)+(',' if index+1<len(values) else ''))
        lines.append('      ]'+(',' if key=='Decisions' else ''))
    lines.append('    }'+(',' if case_index+1<len(cases) else ''))
lines.extend(['  }', '}'])
(root/'recipes-v1.json').write_text('\n'.join(lines)+'\n')
print('Generated upstream-building-block recipe decisions and next-session fills.')
