import unittest
from decimal import Decimal

from engine_reference import reference


class EngineReferenceTests(unittest.TestCase):
    def test_dividend_receivable_and_deposit_never_create_profit(self):
        dates = ['2016-01-04', '2016-01-05', '2016-01-07',
                 '2016-01-08', '2016-01-15', '2016-01-20']
        data = {
            'raw': [{'date': d, 'open': '100' if i < 3 else '99'}
                    for i, d in enumerate(dates)],
            'session_closes': [{'date': d, 'price': '100' if i < 3 else '99'}
                               for i, d in enumerate(dates)],
            'actions': [{'kind': 'cash_dividend', 'ex_date': '2016-01-08',
                         'pay_date': '2016-01-20', 'amount': '1'}],
        }
        result = reference(data, '0')
        self.assertEqual(Decimal(result['final_equity']), Decimal('3000'))
        self.assertTrue(all(Decimal(d['return']) == 0 for d in result['days']))
        self.assertEqual(Decimal(result['twr']), 0)
        stressed = reference(data, '5')
        self.assertLess(Decimal(stressed['twr']), 0)
        self.assertLess(Decimal(stressed['final_equity']), Decimal('3000'))


if __name__ == '__main__':
    unittest.main()
