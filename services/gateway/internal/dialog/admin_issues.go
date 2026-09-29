package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

const adminIssueResolutionFlow = "issue_admin_resolution"

type adminIssueRequest struct {
	recognized bool
	kind       string
	status     string
	page       int
	issueID    string
	version    int64
	target     string
	slot       int
	comment    string
}

type adminIssuesReader interface {
	AdminIssues(context.Context, string, dataapi.AdminIssueFilter) (dataapi.Page[dataapi.Issue], error)
}

type adminIssueEmployeeReader interface {
	AdminEmployee(context.Context, string, string) (dataapi.Employee, error)
}

type adminIssueAssetReader interface {
	AssetContent(context.Context, string, string) (dataapi.AssetContent, error)
}

type adminIssueResolver interface {
	IssueResolve(context.Context, string, string, int64, dataapi.IssueResolveInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
}

func parseAdminIssueEvent(event dataapi.NormalizedEvent) adminIssueRequest {
	if event.EventType == "message_created" && event.Payload.Kind == "text" && event.Payload.Text != nil {
		value := strings.TrimSpace(*event.Payload.Text)
		lower := strings.ToLower(value)
		if lower == "/adminissues" {
			return adminIssueRequest{recognized: true, kind: "list", status: "open", page: 1}
		}
		if strings.HasPrefix(lower, "/adminissues ") {
			status := strings.TrimSpace(strings.TrimPrefix(lower, "/adminissues "))
			if status == "" {
				status = "open"
			}
			return adminIssueRequest{recognized: true, kind: "list", status: status, page: 1}
		}
		if strings.HasPrefix(lower, "комментарий:") {
			return adminIssueRequest{recognized: true, kind: "comment", comment: strings.TrimSpace(value[len("Комментарий:"):])}
		}
		return adminIssueRequest{}
	}
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return adminIssueRequest{}
	}
	parts := strings.Split(*event.Payload.CallbackData, ":")
	if len(parts) == 0 {
		return adminIssueRequest{}
	}
	switch parts[0] {
	case "admin-issues", "admin-issue", "admin-issue-photo", "admin-issue-take", "admin-issue-resume", "admin-issue-cancel", "admin-issue-begin", "admin-issue-confirm":
	default:
		return adminIssueRequest{}
	}
	request := adminIssueRequest{recognized: true, kind: parts[0]}
	switch parts[0] {
	case "admin-issues":
		request.kind = "list"
		if len(parts) == 3 {
			request.status, request.page = parts[1], parsePositiveInt(parts[2])
		}
	case "admin-issue":
		request.kind = "detail"
		if len(parts) == 2 {
			request.issueID = parts[1]
		}
	case "admin-issue-take", "admin-issue-resume", "admin-issue-cancel":
		if parts[0] == "admin-issue-take" {
			request.kind = "take"
		} else if parts[0] == "admin-issue-resume" {
			request.kind = "resume"
		} else {
			request.kind = "cancel"
		}
		if len(parts) == 3 {
			request.issueID, request.version = parts[1], parseAdminIssueVersion(parts[2])
		}
	case "admin-issue-begin", "admin-issue-confirm":
		if parts[0] == "admin-issue-begin" {
			request.kind = "begin"
		} else {
			request.kind = "confirm"
		}
		if len(parts) == 4 {
			request.issueID, request.version, request.target = parts[1], parseAdminIssueVersion(parts[2]), parts[3]
		}
	case "admin-issue-photo":
		request.kind = "photo"
		if len(parts) == 4 {
			request.issueID, request.version, request.slot = parts[1], parseAdminIssueVersion(parts[2]), parsePositiveInt(parts[3])
		}
	}
	return request
}

func parseAdminIssueVersion(value string) int64 {
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 1 {
		return 0
	}
	return version
}

func validAdminIssueStatus(status string) bool {
	switch status {
	case "all", "open", "in_progress", "resolved", "known_nonblocking":
		return true
	default:
		return false
	}
}

func (p Bootstrap) handleAdminIssueRequest(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, request adminIssueRequest) error {
	if employee.Role != "admin" {
		return p.sendView(ctx, maxID, "Раздел замечаний доступен только администратору автопарка.", nil)
	}
	switch request.kind {
	case "list":
		return p.showAdminIssueList(ctx, actor, maxID, request.status, request.page)
	case "detail":
		return p.showAdminIssueDetailByID(ctx, actor, maxID, request.issueID, 0, "")
	case "photo":
		return p.showAdminIssuePhoto(ctx, actor, maxID, request.issueID, request.version, request.slot)
	case "take":
		return p.takeAdminIssue(ctx, item, actor, maxID, employee, request.issueID, request.version)
	case "begin":
		return p.beginAdminIssueResolution(ctx, item, actor, maxID, employee, state, request.issueID, request.version, request.target)
	case "comment":
		return p.saveAdminIssueComment(ctx, item, actor, maxID, employee, state, request.comment)
	case "confirm":
		return p.confirmAdminIssueResolution(ctx, item, actor, maxID, employee, state, request.issueID, request.version, request.target)
	case "cancel":
		return p.cancelAdminIssueResolution(ctx, item, actor, maxID, employee, state, request.issueID, request.version)
	case "resume":
		return p.resumeAdminIssueResolution(ctx, actor, maxID, state, request.issueID, request.version)
	default:
		return p.sendView(ctx, maxID, "Кнопка замечания повреждена или устарела. Откройте /adminissues.", [][]maxsdk.Button{{{Text: "Обновить замечания", Payload: "admin-issues:open:1"}}})
	}
}

func (p Bootstrap) showAdminIssueList(ctx context.Context, actor string, maxID int64, status string, wanted int) error {
	reader, ok := p.Data.(adminIssuesReader)
	if !ok {
		return errors.New("dialog admin issue reader is not configured")
	}
	if !validAdminIssueStatus(status) || wanted < 1 || wanted > 20 {
		return p.sendView(ctx, maxID, "Фильтр или страница замечаний недоступны. Откройте /adminissues.", nil)
	}
	cursor := ""
	var page dataapi.Page[dataapi.Issue]
	for number := 1; number <= wanted; number++ {
		filter := dataapi.AdminIssueFilter{Limit: 5, Cursor: cursor}
		if status != "all" {
			filter.Status = status
		}
		var err error
		page, err = reader.AdminIssues(ctx, actor, filter)
		if err != nil {
			return err
		}
		if number < wanted {
			if page.NextCursor == nil {
				return p.sendView(ctx, maxID, "Больше замечаний нет. Обновите список.", [][]maxsdk.Button{{{Text: "Открытые", Payload: "admin-issues:open:1"}}})
			}
			cursor = *page.NextCursor
		}
	}
	rows := [][]maxsdk.Button{
		{{Text: "Открытые", Payload: "admin-issues:open:1"}, {Text: "В работе", Payload: "admin-issues:in_progress:1"}, {Text: "Все", Payload: "admin-issues:all:1"}},
		{{Text: "Решённые", Payload: "admin-issues:resolved:1"}, {Text: "Неблокирующие", Payload: "admin-issues:known_nonblocking:1"}},
	}
	lines := []string{fmt.Sprintf("Замечания · %s · страница %d", adminIssueFilterLabel(status), wanted)}
	vehicleLabels := map[string]string{}
	employeeLabels := map[string]string{}
	for _, issue := range page.Items {
		if !vehicleIDPattern.MatchString(issue.ID) || !vehicleIDPattern.MatchString(issue.VehicleID) || issue.Version < 1 || issue.Status == "" {
			return errors.New("data-api: invalid admin issue list projection")
		}
		if status != "all" && issue.Status != status {
			return errors.New("data-api: admin issue projection does not match filter")
		}
		vehicleLabel, found := vehicleLabels[issue.VehicleID]
		if !found {
			var err error
			vehicleLabel, err = p.adminIssueVehicleLabel(ctx, actor, issue.VehicleID)
			if err != nil {
				return err
			}
			vehicleLabels[issue.VehicleID] = vehicleLabel
		}
		authorLabel, found := employeeLabels[issue.AuthorID]
		if !found {
			var err error
			authorLabel, err = p.adminIssueEmployeeLabel(ctx, actor, issue.AuthorID)
			if err != nil {
				return err
			}
			employeeLabels[issue.AuthorID] = authorLabel
		}
		label := fmt.Sprintf("%s · %s · %s", vehicleLabel, adminIssueCategoryLabel(issue.Category), adminIssueStatusLabel(issue.Status))
		rows = append(rows, []maxsdk.Button{{Text: shortLabel(label), Payload: "admin-issue:" + issue.ID}})
		lines = append(lines, fmt.Sprintf("Обновлено %s · %s · %s · %s · %s", formatMoment(issue.UpdatedAt, p.Location), vehicleLabel, authorLabel, adminIssueStageLabel(issue.Stage), adminIssueCategoryLabel(issue.Category)))
	}
	controls := []maxsdk.Button{}
	if wanted > 1 {
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("admin-issues:%s:%d", status, wanted-1)})
	}
	if page.NextCursor != nil && wanted < 20 {
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("admin-issues:%s:%d", status, wanted+1)})
	}
	if len(controls) > 0 {
		rows = append(rows, controls)
	}
	if len(page.Items) == 0 {
		lines = append(lines, "Пока нет замечаний с этим фильтром.")
	}
	return p.sendView(ctx, maxID, strings.Join(lines, "\n"), rows)
}

func (p Bootstrap) adminIssueVehicleLabel(ctx context.Context, actor, vehicleID string) (string, error) {
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return "авто " + shortLabel(vehicleID), nil
		}
		return "", err
	}
	if vehicle.ID != vehicleID {
		return "", errors.New("data-api: admin issue vehicle lookup returned another vehicle")
	}
	return oneLine(vehicle.Plate), nil
}

func (p Bootstrap) adminIssueEmployeeLabel(ctx context.Context, actor, employeeID string) (string, error) {
	reader, ok := p.Data.(adminIssueEmployeeReader)
	if !ok {
		return "сотрудник " + shortLabel(employeeID), nil
	}
	employee, err := reader.AdminEmployee(ctx, actor, employeeID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return "сотрудник " + shortLabel(employeeID), nil
		}
		return "", err
	}
	if employee.ID != employeeID {
		return "", errors.New("data-api: admin issue author lookup returned another employee")
	}
	return oneLine(employee.DisplayName), nil
}

func adminIssueFilterLabel(status string) string {
	if status == "all" {
		return "все состояния"
	}
	return adminIssueStatusLabel(status)
}

func adminIssueStatusLabel(status string) string {
	switch status {
	case "open":
		return "открыто"
	case "in_progress":
		return "в работе"
	case "resolved":
		return "устранено"
	case "known_nonblocking":
		return "известное неблокирующее"
	default:
		return shortLabel(status)
	}
}

func adminIssueCategoryLabel(category string) string {
	switch category {
	case "body_damage":
		return "кузов"
	case "mechanical":
		return "механика"
	case "cleanliness":
		return "чистота"
	case "keys":
		return "ключи"
	case "parking":
		return "парковка"
	case "car_lock":
		return "машина не закрывается"
	case "other":
		return "другое"
	default:
		return shortLabel(category)
	}
}

func adminIssueStageLabel(stage string) string {
	switch stage {
	case "before":
		return "до поездки"
	case "during":
		return "во время поездки"
	case "return", "after":
		return "при возврате"
	case "post_return":
		return "после поездки"
	default:
		return shortLabel(stage)
	}
}

func (p Bootstrap) showAdminIssueDetailByID(ctx context.Context, actor string, maxID int64, issueID string, expectedVersion int64, prefix string) error {
	if !vehicleIDPattern.MatchString(issueID) {
		return p.sendView(ctx, maxID, "Некорректная ссылка на замечание. Откройте /adminissues.", nil)
	}
	issue, err := p.Data.Issue(ctx, actor, issueID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Замечание недоступно. Обновите список.", [][]maxsdk.Button{{{Text: "Обновить замечания", Payload: "admin-issues:open:1"}}})
		}
		return err
	}
	if issue.ID != issueID || issue.Version < 1 {
		return errors.New("data-api: invalid admin issue detail projection")
	}
	if expectedVersion > 0 && expectedVersion != issue.Version && prefix == "" {
		prefix = "Замечание изменилось. Ниже актуальные данные.\n"
	}
	return p.showAdminIssueDetail(ctx, actor, maxID, issue, prefix)
}

func (p Bootstrap) showAdminIssueDetail(ctx context.Context, actor string, maxID int64, issue dataapi.Issue, prefix string) error {
	vehicle, err := p.adminIssueVehicleLabel(ctx, actor, issue.VehicleID)
	if err != nil {
		return err
	}
	author, err := p.adminIssueEmployeeLabel(ctx, actor, issue.AuthorID)
	if err != nil {
		return err
	}
	lines := []string{
		"Замечание · " + adminIssueStatusLabel(issue.Status),
		"Автомобиль: " + vehicle,
		"Автор: " + author,
		"Обновлено: " + formatMoment(issue.UpdatedAt, p.Location),
		"Этап: " + adminIssueStageLabel(issue.Stage),
		"Категория: " + adminIssueCategoryLabel(issue.Category),
		"Блокирует выдачу: " + map[bool]string{true: "да", false: "нет"}[issue.BlocksIssuance],
		"Описание: " + issue.Description,
	}
	if issue.AssignedTo != nil {
		assigned, err := p.adminIssueEmployeeLabel(ctx, actor, *issue.AssignedTo)
		if err != nil {
			return err
		}
		lines = append(lines, "Взял в работу: "+assigned)
	}
	if issue.ResolutionComment != nil {
		lines = append(lines, "Комментарий решения: "+*issue.ResolutionComment)
	}
	if issue.ResolvedBy != nil {
		resolvedBy, err := p.adminIssueEmployeeLabel(ctx, actor, *issue.ResolvedBy)
		if err != nil {
			return err
		}
		lines = append(lines, "Решение зафиксировал: "+resolvedBy)
	}
	if issue.ResolvedAt != nil {
		lines = append(lines, "Решено: "+formatMoment(*issue.ResolvedAt, p.Location))
	}
	rows := [][]maxsdk.Button{}
	for slot := 1; slot <= min(3, len(issue.AssetIDs)); slot++ {
		rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("Фото замечания · %d", slot), Payload: fmt.Sprintf("admin-issue-photo:%s:%d:%d", issue.ID, issue.Version, slot)}})
	}
	if issue.Status == "open" {
		rows = append(rows, []maxsdk.Button{{Text: "Взять в работу", Payload: fmt.Sprintf("admin-issue-take:%s:%d", issue.ID, issue.Version)}})
	}
	if issue.Status == "open" || issue.Status == "in_progress" {
		rows = append(rows, []maxsdk.Button{{Text: "Проблема устранена", Payload: fmt.Sprintf("admin-issue-begin:%s:%d:resolved", issue.ID, issue.Version)}})
		rows = append(rows, []maxsdk.Button{{Text: "Известное неблокирующее замечание", Payload: fmt.Sprintf("admin-issue-begin:%s:%d:known_nonblocking", issue.ID, issue.Version)}})
	}
	rows = append(rows, []maxsdk.Button{{Text: "К списку замечаний", Payload: "admin-issues:open:1"}})
	return p.sendView(ctx, maxID, prefix+strings.Join(lines, "\n"), rows)
}

func (p Bootstrap) showAdminIssuePhoto(ctx context.Context, actor string, maxID int64, issueID string, version int64, slot int) error {
	if !vehicleIDPattern.MatchString(issueID) || version < 1 || slot < 1 || slot > 3 {
		return p.sendView(ctx, maxID, "Ссылка на фото замечания устарела. Обновите карточку.", nil)
	}
	issue, err := p.Data.Issue(ctx, actor, issueID)
	if err != nil {
		return p.adminIssuePhotoReadError(ctx, maxID, issueID, version, slot, err)
	}
	if issue.ID != issueID || issue.Version != version || slot > len(issue.AssetIDs) {
		return p.sendView(ctx, maxID, "Замечание или его фото изменились. Обновите карточку.", [][]maxsdk.Button{{{Text: "Открыть замечание", Payload: "admin-issue:" + issueID}}})
	}
	assets, ok := p.Data.(adminIssueAssetReader)
	if !ok {
		return errors.New("dialog admin issue asset reader is not configured")
	}
	sender, ok := p.MAX.(maxsdk.PhotoSender)
	if !ok {
		return errors.New("MAX photo sender is not configured")
	}
	content, err := assets.AssetContent(ctx, actor, issue.AssetIDs[slot-1])
	if err != nil {
		return p.adminIssuePhotoReadError(ctx, maxID, issueID, version, slot, err)
	}
	if len(content.Bytes) == 0 || len(content.Bytes) > 10<<20 || (content.ContentType != "image/jpeg" && content.ContentType != "image/png" && content.ContentType != "image/webp") {
		return errors.New("data-api: invalid admin issue photo content")
	}
	_, err = sender.SendImage(ctx, maxID, fmt.Sprintf("Фото замечания · %d", slot), content.ContentType, content.Bytes)
	return err
}

func (p Bootstrap) adminIssuePhotoReadError(ctx context.Context, maxID int64, issueID string, version int64, slot int, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 403 || apiErr.Status == 404 || apiErr.Status == 409) {
		return p.sendView(ctx, maxID, "Фото замечания недоступно или карточка изменилась. Обновите её и повторите.", [][]maxsdk.Button{{{Text: "Открыть замечание", Payload: "admin-issue:" + issueID}}})
	}
	if errors.As(err, &apiErr) && (apiErr.Status == 503 || apiErr.Status == 429) {
		return p.sendView(ctx, maxID, "Фото временно не удалось прочитать. Карточка и замечание сохранены; повторите просмотр позже.", [][]maxsdk.Button{{{Text: "Открыть замечание", Payload: fmt.Sprintf("admin-issue:%s", issueID)}, {Text: "Повторить фото", Payload: fmt.Sprintf("admin-issue-photo:%s:%d:%d", issueID, version, slot)}}})
	}
	return err
}

func (p Bootstrap) takeAdminIssue(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, issueID string, version int64) error {
	if !vehicleIDPattern.MatchString(issueID) || version < 1 || p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return p.sendView(ctx, maxID, "Кнопка «Взять в работу» устарела. Обновите замечания.", nil)
	}
	resolver, ok := p.Commands.(adminIssueResolver)
	if !ok {
		return errors.New("dialog issue resolver is not configured")
	}
	key, err := inboxworker.CommandKey(item, "issue.resolve")
	if err != nil {
		return err
	}
	result, recovered, err := p.recoverAdminIssueCommand(ctx, actor, key, issueID, "in_progress")
	if err != nil {
		return err
	}
	if !recovered {
		current, readErr := p.Data.Issue(ctx, actor, issueID)
		if readErr != nil {
			var apiErr *dataapi.APIError
			if errors.As(readErr, &apiErr) && apiErr.Status == 404 {
				return p.sendView(ctx, maxID, "Замечание недоступно. Обновите список.", nil)
			}
			return readErr
		}
		if current.Version != version || current.Status != "open" {
			return p.showAdminIssueDetail(ctx, actor, maxID, current, "Замечание уже изменилось. Показываю актуальное состояние.\n")
		}
		result, err = resolver.IssueResolve(ctx, actor, issueID, version, dataapi.IssueResolveInput{Status: "in_progress"}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
		if err != nil {
			var apiErr *dataapi.APIError
			if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
				return p.showAdminIssueDetailByID(ctx, actor, maxID, issueID, 0, "Замечание изменилось до сохранения.\n")
			}
			return err
		}
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || issue.ID != issueID || issue.Status != "in_progress" || issue.AssignedTo == nil || *issue.AssignedTo != employee.ID {
		return errors.New("issue.resolve returned an invalid take-work result")
	}
	return p.showAdminIssueDetail(ctx, actor, maxID, issue, "Замечание назначено вам.\n")
}

func (p Bootstrap) recoverAdminIssueCommand(ctx context.Context, actor, key, issueID, status string) (dataapi.CommandResult, bool, error) {
	reader, ok := p.Data.(ownCommandReader)
	if !ok {
		return dataapi.CommandResult{}, false, errors.New("command result reader is not configured")
	}
	result, err := reader.OwnCommandResult(ctx, actor, key, "issue.resolve")
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return dataapi.CommandResult{}, false, nil
		}
		return dataapi.CommandResult{}, false, err
	}
	issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || issue.ID != issueID || issue.Status != status || issue.Version < 2 {
		return dataapi.CommandResult{}, false, errors.New("saved issue.resolve result does not match the requested action")
	}
	return result, true, nil
}

func (p Bootstrap) beginAdminIssueResolution(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, issueID string, version int64, target string) error {
	if !vehicleIDPattern.MatchString(issueID) || version < 1 || !terminalIssueStatus(target) {
		return p.sendView(ctx, maxID, "Действие с замечанием устарело. Откройте его снова.", nil)
	}
	issue, err := p.Data.Issue(ctx, actor, issueID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Замечание недоступно. Обновите список.", nil)
		}
		return err
	}
	if issue.Version != version || (issue.Status != "open" && issue.Status != "in_progress") {
		return p.showAdminIssueDetail(ctx, actor, maxID, issue, "Замечание изменилось; решение не начато.\n")
	}
	if current := state.Conversation; current != nil {
		if current.Flow == adminIssueResolutionFlow && current.Context.IssueID != nil && *current.Context.IssueID == issueID &&
			current.Context.IssueVersion != nil && *current.Context.IssueVersion == version && current.Step == adminIssueAwaitStep(target) {
			return p.renderAdminIssueResolution(ctx, maxID, current)
		}
		if current.Flow != adminIssueResolutionFlow || current.Step != "done" && current.Step != "cancelled" {
			rows := [][]maxsdk.Button{}
			if current.Flow == adminIssueResolutionFlow && current.Context.IssueID != nil && current.Context.IssueVersion != nil {
				rows = append(rows, []maxsdk.Button{{Text: "Продолжить прежнее решение", Payload: fmt.Sprintf("admin-issue-resume:%s:%d", *current.Context.IssueID, *current.Context.IssueVersion)}})
			}
			return p.sendView(ctx, maxID, "Сначала завершите или отмените сохранённый диалог. Его текст останется сохранён в MAX Fleet.", rows)
		}
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin issue resolution requires a durable inbox lease")
	}
	pending := "text"
	input := dataapi.ConversationSaveInput{Flow: adminIssueResolutionFlow, Step: adminIssueAwaitStep(target), PendingInputKind: &pending,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &version}}
	saved, err := p.saveAdminIssueConversation(ctx, item, actor, employee, state, input)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	return p.renderAdminIssueResolution(ctx, maxID, &saved)
}

func terminalIssueStatus(status string) bool {
	return status == "resolved" || status == "known_nonblocking"
}

func adminIssueAwaitStep(status string) string {
	if status == "known_nonblocking" {
		return "await_comment_known_nonblocking"
	}
	return "await_comment_resolved"
}

func adminIssueConfirmStep(status string) string {
	if status == "known_nonblocking" {
		return "confirm_known_nonblocking"
	}
	return "confirm_resolved"
}

func statusFromAdminIssueStep(step string) string {
	switch step {
	case "await_comment_known_nonblocking", "confirm_known_nonblocking":
		return "known_nonblocking"
	case "await_comment_resolved", "confirm_resolved":
		return "resolved"
	default:
		return ""
	}
}

func (p Bootstrap) renderAdminIssueResolution(ctx context.Context, maxID int64, conversation *dataapi.Conversation) error {
	if conversation == nil || conversation.Flow != adminIssueResolutionFlow || conversation.Context.IssueID == nil || conversation.Context.IssueVersion == nil {
		return p.sendView(ctx, maxID, "Сохранённый диалог замечания неполон. Откройте /adminissues.", nil)
	}
	id, version := *conversation.Context.IssueID, *conversation.Context.IssueVersion
	switch conversation.Step {
	case "await_comment_resolved", "await_comment_known_nonblocking":
		return p.sendView(ctx, maxID, "Добавьте комментарий одним сообщением в формате:\nКомментарий: что проверено или исправлено\n\nРешение не будет сохранено, пока вы не подтвердите его кнопкой.", [][]maxsdk.Button{{{Text: "Отменить решение", Payload: fmt.Sprintf("admin-issue-cancel:%s:%d", id, version)}}})
	case "confirm_resolved", "confirm_known_nonblocking":
		if conversation.Context.DraftText == nil || strings.TrimSpace(*conversation.Context.DraftText) == "" {
			return p.sendView(ctx, maxID, "В сохранённом диалоге нет комментария. Откройте замечание снова.", [][]maxsdk.Button{{{Text: "Открыть замечание", Payload: "admin-issue:" + id}}})
		}
		status := statusFromAdminIssueStep(conversation.Step)
		return p.sendView(ctx, maxID, "Подтвердите решение «"+adminIssueStatusLabel(status)+"»?\nКомментарий: "+*conversation.Context.DraftText,
			[][]maxsdk.Button{{{Text: "Подтвердить", Payload: fmt.Sprintf("admin-issue-confirm:%s:%d:%s", id, version, status)}}, {{Text: "Отменить", Payload: fmt.Sprintf("admin-issue-cancel:%s:%d", id, version)}}})
	default:
		return p.sendView(ctx, maxID, "В этом диалоге больше нет ожидающего решения. Откройте /adminissues.", nil)
	}
}

func (p Bootstrap) saveAdminIssueComment(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, comment string) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminIssueResolutionFlow || conversation.Context.IssueID == nil || conversation.Context.IssueVersion == nil {
		return p.sendView(ctx, maxID, "Сейчас нет ожидающего комментария к замечанию. Откройте /adminissues.", nil)
	}
	if conversation.Step == "confirm_resolved" || conversation.Step == "confirm_known_nonblocking" {
		if conversation.Context.DraftText != nil && *conversation.Context.DraftText == comment {
			return p.renderAdminIssueResolution(ctx, maxID, conversation)
		}
		return p.sendView(ctx, maxID, "Комментарий уже сохранён. Чтобы изменить его, отмените решение и начните снова.", [][]maxsdk.Button{{{Text: "Отменить решение", Payload: fmt.Sprintf("admin-issue-cancel:%s:%d", *conversation.Context.IssueID, *conversation.Context.IssueVersion)}}})
	}
	status := statusFromAdminIssueStep(conversation.Step)
	if status == "" {
		return p.sendView(ctx, maxID, "Шаг комментария устарел. Откройте замечание снова.", nil)
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) == 0 || utf8.RuneCountInString(comment) > 1000 {
		return p.sendView(ctx, maxID, "Комментарий обязателен и должен содержать не более 1000 знаков. Повторите: Комментарий: ...", nil)
	}
	issue, err := p.Data.Issue(ctx, actor, *conversation.Context.IssueID)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, *conversation.Context.IssueID, err)
	}
	if issue.Version != *conversation.Context.IssueVersion || issue.Status != "open" && issue.Status != "in_progress" {
		return p.showAdminIssueDetail(ctx, actor, maxID, issue, "Замечание изменилось. Комментарий не принят; откройте карточку заново.\n")
	}
	pending := "none"
	version := *conversation.Context.IssueVersion
	issueID := *conversation.Context.IssueID
	input := dataapi.ConversationSaveInput{Flow: adminIssueResolutionFlow, Step: adminIssueConfirmStep(status), PendingInputKind: &pending,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &version, DraftText: &comment}}
	saved, err := p.saveAdminIssueConversation(ctx, item, actor, employee, state, input)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	return p.renderAdminIssueResolution(ctx, maxID, &saved)
}

func (p Bootstrap) saveAdminIssueConversation(ctx context.Context, item dataapi.InboxClaimItem, actor string, employee dataapi.Employee, state dataapi.CurrentState, input dataapi.ConversationSaveInput) (dataapi.Conversation, error) {
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return dataapi.Conversation{}, errors.New("admin issue conversation save requires an inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "conversation.save")
	if err != nil {
		return dataapi.Conversation{}, err
	}
	result, err := p.Commands.ConversationSave(ctx, actor, employee.ID, state.ConversationVersion, input, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		return dataapi.Conversation{}, err
	}
	saved, err := dataapi.DecodeAggregate[dataapi.Conversation](result)
	if err != nil || saved.Flow != adminIssueResolutionFlow || saved.Step != input.Step || saved.Version != state.ConversationVersion+1 || saved.Context.IssueID == nil || saved.Context.IssueVersion == nil ||
		*saved.Context.IssueID != *input.Context.IssueID || *saved.Context.IssueVersion != *input.Context.IssueVersion {
		return dataapi.Conversation{}, errors.New("conversation.save returned an invalid admin issue state")
	}
	return saved, nil
}

func (p Bootstrap) adminIssueConversationSaveError(ctx context.Context, maxID int64, issueID string, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
		return p.sendView(ctx, maxID, "Замечание или сохранённый диалог изменились. Обновите карточку.", [][]maxsdk.Button{{{Text: "Открыть замечание", Payload: "admin-issue:" + issueID}}})
	}
	return err
}

func (p Bootstrap) resumeAdminIssueResolution(ctx context.Context, actor string, maxID int64, state dataapi.CurrentState, issueID string, version int64) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminIssueResolutionFlow || conversation.Context.IssueID == nil || conversation.Context.IssueVersion == nil ||
		!vehicleIDPattern.MatchString(issueID) || version < 1 || *conversation.Context.IssueID != issueID || *conversation.Context.IssueVersion != version {
		return p.sendView(ctx, maxID, "Сохранённый диалог уже изменился. Откройте /adminissues.", nil)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.showAdminIssueDetailByID(ctx, actor, maxID, issueID, 0, "")
	}
	issue, err := p.Data.Issue(ctx, actor, issueID)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	if issue.Version != version || issue.Status != "open" && issue.Status != "in_progress" {
		return p.showAdminIssueDetail(ctx, actor, maxID, issue, "Замечание изменилось, старое подтверждение больше не действует. Отмените сохранённый шаг и начните решение заново.\n")
	}
	return p.renderAdminIssueResolution(ctx, maxID, conversation)
}

func (p Bootstrap) cancelAdminIssueResolution(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, issueID string, version int64) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminIssueResolutionFlow || conversation.Context.IssueID == nil || conversation.Context.IssueVersion == nil ||
		!vehicleIDPattern.MatchString(issueID) || version < 1 || *conversation.Context.IssueID != issueID || *conversation.Context.IssueVersion != version {
		return p.sendView(ctx, maxID, "Диалог решения уже изменился. Откройте /adminissues.", nil)
	}
	issue, err := p.Data.Issue(ctx, actor, issueID)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	if conversation.Step == "done" || conversation.Step == "cancelled" {
		return p.showAdminIssueDetail(ctx, actor, maxID, issue, "")
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin issue cancellation requires a durable inbox lease")
	}
	issueVersion := issue.Version
	step := "cancelled"
	if terminalIssueStatus(issue.Status) {
		step = "done"
	}
	pending := "none"
	input := dataapi.ConversationSaveInput{Flow: adminIssueResolutionFlow, Step: step, PendingInputKind: &pending,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &issueVersion}}
	saved, err := p.saveAdminIssueConversation(ctx, item, actor, employee, state, input)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	message := "Решение отменено. Замечание не менялось."
	if step == "done" {
		message = "Пока вы писали комментарий, замечание уже было решено. Показываю текущее состояние."
	}
	_ = saved
	return p.showAdminIssueDetail(ctx, actor, maxID, issue, message+"\n")
}

func (p Bootstrap) confirmAdminIssueResolution(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, issueID string, version int64, target string) error {
	conversation := state.Conversation
	if conversation == nil || conversation.Flow != adminIssueResolutionFlow || conversation.Context.IssueID == nil || conversation.Context.IssueVersion == nil ||
		!vehicleIDPattern.MatchString(issueID) || version < 1 || !terminalIssueStatus(target) || *conversation.Context.IssueID != issueID ||
		*conversation.Context.IssueVersion != version || conversation.Step != adminIssueConfirmStep(target) || conversation.Context.DraftText == nil || strings.TrimSpace(*conversation.Context.DraftText) == "" {
		if conversation != nil && conversation.Flow == adminIssueResolutionFlow && conversation.Context.IssueID != nil {
			return p.showAdminIssueDetailByID(ctx, actor, maxID, *conversation.Context.IssueID, 0, "Подтверждение устарело. Откройте актуальную карточку.\n")
		}
		return p.sendView(ctx, maxID, "Подтверждение устарело. Откройте /adminissues и проверьте замечание заново.", nil)
	}
	if p.Commands == nil || item.ID == "" || item.LeaseToken == "" {
		return errors.New("admin issue confirmation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "issue.resolve")
	if err != nil {
		return err
	}
	result, recovered, err := p.recoverAdminIssueCommand(ctx, actor, key, issueID, target)
	if err != nil {
		return err
	}
	if !recovered {
		current, readErr := p.Data.Issue(ctx, actor, issueID)
		if readErr != nil {
			return p.adminIssueConversationSaveError(ctx, maxID, issueID, readErr)
		}
		if current.Version != version || current.Status != "open" && current.Status != "in_progress" {
			return p.showAdminIssueDetail(ctx, actor, maxID, current, "Замечание изменилось. Подтверждение не применено; проверьте актуальную карточку.\n")
		}
		resolver, ok := p.Commands.(adminIssueResolver)
		if !ok {
			return errors.New("dialog issue resolver is not configured")
		}
		result, err = resolver.IssueResolve(ctx, actor, issueID, version, dataapi.IssueResolveInput{Status: target, Comment: *conversation.Context.DraftText, Confirmation: true}, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
		if err != nil {
			var apiErr *dataapi.APIError
			if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
				return p.showAdminIssueDetailByID(ctx, actor, maxID, issueID, 0, "Замечание изменилось до подтверждения.\n")
			}
			return err
		}
	}
	resolved, err := dataapi.DecodeAggregate[dataapi.Issue](result)
	if err != nil || resolved.ID != issueID || resolved.Status != target || resolved.Version <= version || resolved.ResolvedBy == nil || *resolved.ResolvedBy != employee.ID ||
		resolved.ResolutionComment == nil || *resolved.ResolutionComment != *conversation.Context.DraftText {
		return errors.New("issue.resolve returned an invalid terminal result")
	}
	issueVersion := resolved.Version
	pending := "none"
	completedState := dataapi.ConversationSaveInput{Flow: adminIssueResolutionFlow, Step: "done", PendingInputKind: &pending,
		Context: dataapi.ConversationContext{IssueID: &issueID, IssueVersion: &issueVersion}}
	saved, err := p.saveAdminIssueConversation(ctx, item, actor, employee, state, completedState)
	if err != nil {
		return p.adminIssueConversationSaveError(ctx, maxID, issueID, err)
	}
	_ = saved
	return p.showAdminIssueDetail(ctx, actor, maxID, resolved, "Решение сохранено.\n")
}
