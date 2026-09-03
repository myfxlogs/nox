package cmc

// JSON 解码结构体（数值字段全为字符串编码，照抄 API端点清单.md 原文）。
// ⚠️ 数值字段声明为 string 再 strconv.ParseFloat——直接 unmarshal 进 float64 会报错（v7）。

type priceJSON struct {
	QuoteID      string `json:"quoteId"`
	AskPrice     string `json:"askPrice"`
	MidPrice     string `json:"midPrice"`
	BidPrice     string `json:"bidPrice"`
	MarketStatus string `json:"marketStatus"`
	Time         string `json:"time"`
	State        string `json:"state"`
}

type accountSummaryJSON struct {
	PrimaryAccountCurrency   string `json:"primaryAccountCurrency"`
	TotalAccountValue        string `json:"totalAccountValue"`
	TotalEquityFreeForMargin string `json:"totalEquityFreeForMarginTrading"`
	TotalEquityUsedInMargin  string `json:"totalEquityUsedInMarginTrading"`
	TotalCashBalance         string `json:"totalCashBalance"`
	TotalUnrealisedPnL       string `json:"totalUnrealisedPnL"`
	TotalCFDUnrealisedPnL    string `json:"totalCFDUnrealisedPnL"`
}

type positionsJSON struct {
	PrimaryAccountCurrency    string         `json:"primaryAccountCurrency"`
	TotalCFDUnrealisedPnL     string         `json:"totalCFDUnrealisedPnL"`
	TotalStandardMarginAmount string         `json:"totalStandardMarginAmount"`
	Positions                 []positionJSON `json:"positions"`
}

type positionJSON struct {
	InstrumentID                   string      `json:"instrumentId"`
	Direction                      string      `json:"direction"`
	Quantity                       string      `json:"quantity"`
	Price                          string      `json:"price"`
	StandardMarginAmount           string      `json:"standardMarginAmount"`
	MarginAmount                   string      `json:"marginAmount"`
	StandardMarginPercentage       string      `json:"standardMarginPercentage"`
	UnrealisedPnLInPrimaryCurrency string      `json:"unrealisedPnLInPrimaryCurrency"`
	EvaluationBidPrice             string      `json:"evaluationBidPrice"`
	EvaluationAskPrice             string      `json:"evaluationAskPrice"`
	Orders                         []orderJSON `json:"orders"` // D-208 v2-A：orders[0] 含 submittedTimestamp/orderId/filledPrice
}

// orderJSON 持仓关联订单（D-208 v2-A：AccountCollector 解析 orders[0]）。
type orderJSON struct {
	OrderID           string `json:"orderId"`
	FilledPrice       string `json:"filledPrice"`
	SubmittedTimestamp string `json:"submittedTimestamp"` // RFC3339，可能含 7 位小数秒
}

type historyJSON struct {
	TotalCount   int             `json:"totalCount"`
	Transactions []historyTxJSON `json:"transactions"`
}

type historyTxJSON struct {
	TransactionID   string `json:"transactionId"`
	Amount          string `json:"amount"`
	Balance         string `json:"balance"`
	Currency        string `json:"currency"`
	OrderID         string `json:"orderId"`
	TransactionTime string `json:"transactionTime"`
	TransactionType string `json:"transactionType"`
}
