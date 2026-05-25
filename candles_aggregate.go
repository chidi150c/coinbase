// FILE: candles_aggregate.go
package main

import "time"

func AggregateCandles(src []Candle, bucket time.Duration) []Candle {
	if len(src) == 0 || bucket <= 0 {
		return nil
	}

	out := make([]Candle, 0, len(src)/3+1)

	var cur Candle
	var curBucket time.Time
	started := false

	for _, c := range src {
		if c.Time.IsZero() || c.Close <= 0 {
			continue
		}

		bucketStart := c.Time.UTC().Truncate(bucket)

		if !started || !bucketStart.Equal(curBucket) {
			if started {
				out = append(out, cur)
			}

			curBucket = bucketStart
			cur = Candle{
				Time:   bucketStart,
				Open:   c.Open,
				High:   c.High,
				Low:    c.Low,
				Close:  c.Close,
				Volume: c.Volume,
			}
			started = true
			continue
		}

		if cur.Open <= 0 {
			cur.Open = c.Open
		}
		if c.High > cur.High {
			cur.High = c.High
		}
		if c.Low < cur.Low || cur.Low == 0 {
			cur.Low = c.Low
		}
		cur.Close = c.Close
		cur.Volume += c.Volume
	}

	if started {
		out = append(out, cur)
	}

	return out
}
