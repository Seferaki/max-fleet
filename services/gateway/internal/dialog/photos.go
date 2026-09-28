package dialog

import (
	"context"
	"fmt"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

var photoAngles = [8]string{
	"автомобиль спереди",
	"автомобиль сзади",
	"левый борт",
	"правый борт",
	"передняя часть салона",
	"задняя часть салона",
	"багажник",
	"приборная панель",
}

// The projection from Python is the only source of photo progress. An invalid
// projection must not lead to a guessed slot or a successful photo claim.
func photoSlot(inspection dataapi.Inspection) (int, int, bool) {
	if inspection.ID == "" || inspection.Phase != "before" || inspection.Status != "draft" {
		return 0, 0, false
	}
	var occupied [8]bool
	for _, slot := range inspection.OccupiedSlots {
		if slot < 1 || slot > 8 || occupied[slot-1] {
			return 0, 0, false
		}
		occupied[slot-1] = true
	}
	for i, taken := range occupied {
		if !taken {
			return i + 1, len(inspection.OccupiedSlots), true
		}
	}
	return 0, 8, true
}

func photoProgress(inspection dataapi.Inspection) string {
	slot, count, ok := photoSlot(inspection)
	if !ok {
		return "Состояние осмотра недоступно. Обновите /menu."
	}
	if slot == 0 {
		return "Фото 8/8 сохранены. Девятое фото не добавляется; для изменения выберите замену ракурса."
	}
	return fmt.Sprintf("Сохранено %d/8. Следующее: фото %d/8 — %s.", count, slot, photoAngles[slot-1])
}

func (p Bootstrap) checkoutPhotos(ctx context.Context, maxID int64, state dataapi.CurrentState, checkoutID string, version int64) error {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 || checkout == nil || checkout.ID != checkoutID || checkout.Version != version || checkout.Status != "holding" || checkout.Step != "inspection" {
		return p.sendView(ctx, maxID, "Шаг осмотра изменился или hold истёк. Обновите /menu.", nil)
	}
	slot, count, ok := photoSlot(checkout.Inspection)
	if !ok {
		return p.sendView(ctx, maxID, "Состояние осмотра недоступно. Обновите /menu.", nil)
	}
	if slot == 0 {
		return p.sendView(ctx, maxID, "Фото 8/8 уже сохранены. Девятое фото не добавится; далее можно подтвердить комплект или выбрать ракурс для замены.", nil)
	}
	return p.sendView(ctx, maxID, fmt.Sprintf("Сохранено %d/8. Фото %d/8 — %s. Отправьте одно изображение для этого ракурса.", count, slot, photoAngles[slot-1]), nil)
}
