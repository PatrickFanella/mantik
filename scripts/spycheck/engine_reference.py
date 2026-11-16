"""Independent Decimal SPY accounting oracle for the engine's next-open check.

No Mantik code is called. Prices use the manifest-bound primary-close projection;
original response bytes and acquisition clocks remain in the private input.
"""
import argparse
from datetime import date, datetime, time, timedelta
from decimal import Decimal, ROUND_DOWN, ROUND_HALF_UP, getcontext
import hashlib
import json
from pathlib import Path
from zoneinfo import ZoneInfo

getcontext().prec = 48
D = Decimal
NY, CHI = ZoneInfo('America/New_York'), ZoneInfo('America/Chicago')


def reference(data, slip_bps):
    bars = data['raw']
    closes = {x['date']: D(x['price']) for x in data['session_closes']}
    cash, shares, growth, previous = D(2000), D(0), D(1), D(2000)
    quantum, share_quantum = D('0.000000000001'), D('0.000000001')
    opened = datetime(2016, 1, 1, tzinfo=CHI)
    deposit = datetime.combine(opened.date(), time(6), CHI)
    while deposit.weekday() != 4 or deposit < opened:
        deposit += timedelta(days=1)
    actions, owed, results = data['actions'], [], []
    last_date = None
    for i, bar in enumerate(bars):
        day = date.fromisoformat(bar['date'])
        opening = D(bar['open'])
        if last_date is not None:
            for action in actions:
                if last_date < action['ex_date'] <= bar['date']:
                    if action['kind'] == 'cash_dividend':
                        owed.append((action['pay_date'], (shares * D(action['amount'])).quantize(quantum, rounding=ROUND_HALF_UP)))
                    else:
                        raise ValueError('SPY check does not silently support a split')
        cash += sum((amount for pay, amount in owed if pay <= bar['date']), D(0))
        owed = [(pay, amount) for pay, amount in owed if pay > bar['date']]
        receivable = sum((amount for _, amount in owed), D(0))
        before = cash + shares * opening + receivable
        flow = D(0)
        at_open = datetime.combine(day, time(9, 30), NY)
        while deposit <= at_open:
            flow += 500
            deposit += timedelta(days=14)
        cash += flow
        if i > 0:
            price = opening * (1 + D(slip_bps) / 10000)
            target = ((before + flow) / opening).quantize(share_quantum, rounding=ROUND_DOWN)
            wanted = max(D(0), target - shares)
            affordable = (cash / price).quantize(share_quantum, rounding=ROUND_DOWN)
            quantity = min(wanted, affordable)
            shares += quantity
            cash -= quantity * price
        equity = cash + shares * closes[bar['date']] + receivable
        today = ((before / previous) * (equity / (before + flow)) - 1).quantize(quantum, rounding=ROUND_HALF_UP)
        growth = (growth * (1 + today)).quantize(quantum, rounding=ROUND_HALF_UP)
        results.append({'date': bar['date'], 'return': str(today), 'twr': str(growth - 1)})
        previous, last_date = equity, bar['date']
    return {'days': results, 'final_equity': str(previous), 'twr': str(growth - 1)}


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    raw = args.input.read_bytes()
    data = json.loads(raw)
    assert data['calendar'][0] == '2016-01-04'
    report = {'version': 'spy-next-open-decimal-receivables-v2', 'input_sha256': hashlib.sha256(raw).hexdigest(),
              'initial_cash': '2000', 'deposit_amount': '500', 'every_weeks': 2,
              'zero_cost': reference(data, '0'), 'modeled_cost': reference(data, '5')}
    args.output.write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
