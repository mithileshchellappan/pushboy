package storage

import "github.com/mithileshchellappan/pushboy/internal/model"

type pushOutcomeDelta struct {
	success int
	failure int
}

func summarizePushReceipts(receipts []model.DeliveryReceipt) (map[string]*pushOutcomeDelta, []model.DeliveryReceipt) {
	deltas := make(map[string]*pushOutcomeDelta)
	failureReceipts := make([]model.DeliveryReceipt, 0)

	for _, receipt := range receipts {
		if _, ok := deltas[receipt.JobID]; !ok {
			deltas[receipt.JobID] = &pushOutcomeDelta{}
		}

		switch receipt.Status {
		case model.DeliveryStatusSuccess:
			deltas[receipt.JobID].success++
		case model.DeliveryStatusFailed:
			deltas[receipt.JobID].failure++
			failureReceipts = append(failureReceipts, receipt)
		}
	}

	return deltas, failureReceipts
}
