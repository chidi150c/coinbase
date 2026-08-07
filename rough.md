entry, err := t.buildPendingEntry(
	intent,
	orderID,
)
if err != nil {
	cancelErr := t.broker.CancelOrder(
		ctx,
		intent.ProductID,
		orderID,
	)

	if cancelErr != nil {
		log.Printf(
			"[ERROR] producer.build_pending.cancel_failed "+
				"producer=%s side=%s order_id=%s "+
				"build_err=%q cancel_err=%q",
			intent.Producer,
			intent.Side,
			orderID,
			err,
			cancelErr,
		)
	}

	return nil, fmt.Errorf(
		"build pending entry order_id=%s: %w",
		orderID,
		err,
	)
}


switch intent.PendingCancelPolicy {
case PendingSignalCancelOnFlatOrOpposite,
	PendingSignalCancelOnOpposite,
	PendingSignalCancelDisabled:
	// valid

case PendingSignalCancelUnspecified:
	return fmt.Errorf(
		"validate pending intent: missing PendingCancelPolicy "+
			"for Producer=%q",
		intent.Producer,
	)

default:
	return fmt.Errorf(
		"validate pending intent: invalid PendingCancelPolicy=%q "+
			"for Producer=%q",
		intent.PendingCancelPolicy,
		intent.Producer,
	)
}