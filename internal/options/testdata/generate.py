"""Generate European-model worked examples with QuantLib 1.40 (BSD-3-Clause)."""
import json
from pathlib import Path
import QuantLib as ql

assert ql.__version__ == "1.40"
today = ql.Date(15, 1, 2026)
ql.Settings.instance().evaluationDate = today
dc = ql.Actual365Fixed()
cases = []
for kind, spot, strike, days, rate, dividend, vol in [
    ("call", 100, 100, 365, .05, 0, .2),
    ("put", 100, 100, 365, .05, 0, .2),
    ("call", 100, 110, 180, .03, .02, .35),
    ("put", 100, 90, 30, -.01, .03, .4),
    ("call", 600, 610, 7, .04, .01, .25),
    ("put", 600, 610, 7, .04, .01, .25),
]:
    process = ql.BlackScholesMertonProcess(
        ql.QuoteHandle(ql.SimpleQuote(spot)),
        ql.YieldTermStructureHandle(ql.FlatForward(today, dividend, dc)),
        ql.YieldTermStructureHandle(ql.FlatForward(today, rate, dc)),
        ql.BlackVolTermStructureHandle(ql.BlackConstantVol(today, ql.NullCalendar(), vol, dc)))
    option = ql.VanillaOption(ql.PlainVanillaPayoff(ql.Option.Call if kind == "call" else ql.Option.Put, strike),
                              ql.EuropeanExercise(today + days))
    option.setPricingEngine(ql.AnalyticEuropeanEngine(process))
    cases.append(dict(input=dict(Type=kind, Spot=spot, Strike=strike, Years=days/365, Rate=rate, Yield=dividend, Volatility=vol),
                      expected=dict(Price=option.NPV(), Delta=option.delta(), Gamma=option.gamma(), Vega=option.vega(), Theta=option.thetaPerDay())))
Path(__file__).with_name("quantlib-v1.json").write_text(json.dumps(cases, indent=2, allow_nan=False) + "\n")
