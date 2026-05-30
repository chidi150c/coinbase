cd ~/coinbase/monitoring

docker compose logs --since "6h" bot_binance \
| grep -oE 'macdLine_5m=[-0-9.]+' \
| sed 's/macdLine_5m=//' \
| awk '
{
  v=$1
  if (v<0) v=-v
  a[++n]=v
}
END {
  if (n==0) {
    print "no macdLine_5m found"
    exit
  }

  asort(a)

  max=a[n]
  eps60=max*0.60

  print "count=" n
  print "p50=" a[int(n*0.50)]
  print "p70=" a[int(n*0.70)]
  print "p80=" a[int(n*0.80)]
  print "p90=" a[int(n*0.90)]
  print "max=" max
  print "eps60=" eps60
}'