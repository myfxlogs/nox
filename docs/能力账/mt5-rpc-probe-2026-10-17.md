# mt5 RPC 实测能力账（harness 直出禁手编）

生成：2026-10-17T09:31:31Z｜网关：mt5grpc.mtapi.io:443｜行数：243

| service.rpc | venue | status | ms | errcode | keyfields |
|---|---|---|---|---|---|
| Connection.Connect | 3001157 | ok | 336 |  | token |
| MT5.Account | 3001157 | ok | 172 |  | result |
| MT5.AccountSummary | 3001157 | ok | 174 |  | result |
| MT5.OpenedOrders | 3001157 | ok | 172 |  |  |
| MT5.OpenedOrdersTickets | 3001157 | ok | 168 |  |  |
| MT5.OrderHistory | 3001157 | ok | 283 |  | result |
| MT5.PendingOrderHistory | 3001157 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.OrderHistoryPagination | 3001157 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.Symbols | 3001157 | ok | 336 |  | result |
| MT5.SymbolList | 3001157 | ok | 170 |  | result |
| MT5.GetQuote | 3001157 | err | 168 | Symbol XAUUSD not exist (:3001157) |  |
| MT5.GetQuoteMany | 3001157 | err | 172 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| MT5.MarketWatchMany | 3001157 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.SymbolParams | 3001157 | err | 170 | Symbol not found: XAUUSD (:3001157) |  |
| MT5.SymbolParamsMany | 3001157 | err | 169 | Symbol not found: XAUUSD (:3001157) |  |
| MT5.ServerTimezone | 3001157 | ok | 169 |  |  |
| MT5.IsTradeSession | 3001157 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:3001157) |  |
| MT5.IsQuoteSession | 3001157 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:3001157) |  |
| MT5.GetTickValueMany | 3001157 | err | 169 | Symbol not found: XAUUSD (:3001157) |  |
| MT5.Mails | 3001157 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.RequiredMargin | 3001157 | err | 169 | Symbol XAUUSD not exist (:3001157) |  |
| QuoteHistory.PriceHistory | 3001157 | err | 169 | XAUUSD not exist (:3001157) |  |
| QuoteHistory.PriceHistoryToday | 3001157 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| Service.Ping | 3001157 | ok | 168 |  | result |
| Service.Health | 3001157 | err | 167 | rpc error: code = Unimplemented desc =  |  |
| Service.Version | 3001157 | ok | 168 |  | result |
| Service.Search | 3001157 | ok | 206 |  | result |
| Subscriptions.Subscribe | 3001157 | ok | 0 |  | symbol |
| Streams.Events | 3001157 | ok | 168 | frames=0 first= | frames=0 |
| MT5.IsTradeSessionMany | 3001157 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:3001157) |  |
| MT5.IsQuoteSessionMany | 3001157 | err | 168 | The given key 'XAUUSD' was not present in the dictionary. (:3001157) |  |
| MT5.SymbolSessionsEx | 3001157 | err | 168 | XAUUSD not exist (:3001157) |  |
| MT5.SymbolSessionsExMany | 3001157 | err | 169 | System.String[] not exist (:3001157) |  |
| MT5.TickValueWithSize | 3001157 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| MT5.OpenedOrder | 3001157 | err | 168 | OK null response |  |
| Subscriptions.SubscribeMany | 3001157 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| Subscriptions.UnSubscribeMany | 3001157 | ok | 168 |  | result |
| Streams.OnTickValue | 3001157 | stream | 12013 | frames=0 | frames=0 |
| Streams.OnOrderProfit | 3001157 | stream | 12007 | frames=0 | frames=0 |
| Streams.OnMarketWatch | 3001157 | stream | 169 | frames=0 | frames=0 |
| Streams.OnOpenedOrdersTickets | 3001157 | stream | 168 | frames=0 | frames=0 |
| Connection.Disconnect | 3001157 | ok | 0 |  | session-closed |
| QuoteHistory.PriceHistoryMonth | 3001157 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| QuoteHistory.PriceHistoryMonthMany | 3001157 | err | 170 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| QuoteHistory.PriceHistoryTodayMany | 3001157 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:3001157) |  |
| QuoteHistory.PriceHistoryMany | 3001157 | ok | 169 |  | result |
| QuoteHistory.PriceHistoryHighLow | 3001157 | err | 169 | XAUUSD not exist (:3001157) |  |
| QuoteHistory.PriceHistoryEx | 3001157 | err | 175 | XAUUSD not exist (:3001157) |  |
| QuoteHistory.PriceHistoryExMany | 3001157 | ok | 169 |  | result |
| Service.PingHost | 3001157 | ok | 174 |  | result |
| Service.PingHostMany | 3001157 | err | 173 | host.Length != port.Length (:0) |  |
| Service.MemorySnapshot | 3001157 | err | 10317 | Unknown result type: { } |  |
| Service.GetClients | 3001157 | err | 169 | Unable to cast object of type 'System.Collections.Generic.LinkedList`1[mt5rest.ClientStatus]' to type 'mt5rest.ClientStatus[]'. |  |
| Service.GetDemo | 3001157 | err | 0 | side-effect（开 demo 户）——skipped 如实缺 |  |
| Connection.CheckConnect | 3001157 | err | 0 | CheckConnect 需独立连接 id（Connect token 径不可复用）——skipped 如实缺 |  |
| Subscriptions.SubscribeOrderProfit | 3001157 | ok | 168 |  | subscribed |
| Subscriptions.SubscribeTickValue | 3001157 | ok | 169 |  | subscribed |
| Subscriptions.SubscribeOrderUpdate | 3001157 | ok | 171 |  | subscribed |
| Subscriptions.SubscribeMarketWatch | 3001157 | err | 169 | rpc error: code = Unimplemented desc =  |  |
| Subscriptions.SubscribeOpenedOrdersTickets | 3001157 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Streams.OnOrderUpdate | 3001157 | stream | 12010 | frames=0 (window 12s) | frames=0 |
| Streams.OnQuote | 3001157 | stream | 12010 | frames=0 (window 12s) | frames=0 |
| Streams.OnMail | 3001157 | stream | 169 | frames=0 (window 12s) | frames=0 |
| TickHistory.TickHistoryRequest | 3001157 | ok | 169 |  | ticket |
| TickHistory.OnTickHistory | 3001157 | stream | 12011 | frames=0 | frames=0 |
| TickHistory.TickHistoryStop | 3001157 | ok | 0 |  | stopped |
| Connection.ConnectEx | 3001157 | skipped | 0 | 连接变体（probe 走 Connect 主径）——未单独实测 |  |
| Connection.ConnectProxy | 3001157 | skipped | 0 | 代理连接变体——未单独实测 |  |
| Connection.Connect | 15032313 | ok | 470 |  | token |
| MT5.Account | 15032313 | ok | 170 |  | result |
| MT5.AccountSummary | 15032313 | ok | 171 |  | result |
| MT5.OpenedOrders | 15032313 | ok | 170 |  |  |
| MT5.OpenedOrdersTickets | 15032313 | ok | 168 |  |  |
| MT5.OrderHistory | 15032313 | ok | 270 |  | result |
| MT5.PendingOrderHistory | 15032313 | err | 176 | rpc error: code = Unimplemented desc =  |  |
| MT5.OrderHistoryPagination | 15032313 | err | 173 | rpc error: code = Unimplemented desc =  |  |
| MT5.Symbols | 15032313 | ok | 4575 |  | result |
| MT5.SymbolList | 15032313 | ok | 708 |  | result |
| MT5.GetQuote | 15032313 | ok | 169 |  | result |
| MT5.GetQuoteMany | 15032313 | ok | 168 |  | result |
| MT5.MarketWatchMany | 15032313 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.SymbolParams | 15032313 | ok | 177 |  | result |
| MT5.SymbolParamsMany | 15032313 | ok | 171 |  | result |
| MT5.ServerTimezone | 15032313 | ok | 172 |  | result |
| MT5.IsTradeSession | 15032313 | ok | 172 |  |  |
| MT5.IsQuoteSession | 15032313 | ok | 168 |  |  |
| MT5.GetTickValueMany | 15032313 | err | 1169 | Unable to cast object of type 'System.Collections.Generic.Dictionary`2[System.String,System.Double]' to type 'System.Double[]'. |  |
| MT5.Mails | 15032313 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.RequiredMargin | 15032313 | ok | 168 |  | result |
| QuoteHistory.PriceHistory | 15032313 | ok | 186 |  | result |
| QuoteHistory.PriceHistoryToday | 15032313 | err | 177 | Unable to cast object of type 'System.Collections.Generic.List`1[mtapi.mt5.Bar]' to type 'mtapi.mt5.Bar[]'. |  |
| Service.Ping | 15032313 | ok | 168 |  | result |
| Service.Health | 15032313 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Service.Version | 15032313 | ok | 176 |  | result |
| Service.Search | 15032313 | ok | 213 |  | result |
| Subscriptions.Subscribe | 15032313 | ok | 0 |  | symbol |
| Streams.Events | 15032313 | ok | 168 | frames=0 first= | frames=0 |
| MT5.IsTradeSessionMany | 15032313 | ok | 171 |  | result |
| MT5.IsQuoteSessionMany | 15032313 | ok | 169 |  | result |
| MT5.SymbolSessionsEx | 15032313 | ok | 170 |  | result |
| MT5.SymbolSessionsExMany | 15032313 | ok | 170 |  | result |
| MT5.TickValueWithSize | 15032313 | ok | 168 |  | result |
| MT5.OpenedOrder | 15032313 | err | 169 | OK null response |  |
| Subscriptions.SubscribeMany | 15032313 | ok | 171 |  | result |
| Subscriptions.UnSubscribeMany | 15032313 | ok | 168 |  | result |
| Streams.OnTickValue | 15032313 | stream | 12011 | frames=0 | frames=0 |
| Streams.OnOrderProfit | 15032313 | stream | 12000 | frames=0 | frames=0 |
| Streams.OnMarketWatch | 15032313 | stream | 171 | frames=0 | frames=0 |
| Streams.OnOpenedOrdersTickets | 15032313 | stream | 169 | frames=0 | frames=0 |
| Connection.Disconnect | 15032313 | ok | 0 |  | session-closed |
| QuoteHistory.PriceHistoryMonth | 15032313 | err | 187 | Unable to cast object of type 'System.Collections.Generic.List`1[mtapi.mt5.Bar]' to type 'mtapi.mt5.Bar[]'. |  |
| QuoteHistory.PriceHistoryMonthMany | 15032313 | err | 187 | Parameter count mismatch. |  |
| QuoteHistory.PriceHistoryTodayMany | 15032313 | err | 179 | Parameter count mismatch. |  |
| QuoteHistory.PriceHistoryMany | 15032313 | ok | 186 |  | result |
| QuoteHistory.PriceHistoryHighLow | 15032313 | err | 184 | Attempted to divide by zero. (:15032313) |  |
| QuoteHistory.PriceHistoryEx | 15032313 | ok | 1724 |  | result |
| QuoteHistory.PriceHistoryExMany | 15032313 | ok | 1814 |  | result |
| Service.PingHost | 15032313 | ok | 174 |  | result |
| Service.PingHostMany | 15032313 | err | 168 | host.Length != port.Length (:0) |  |
| Service.MemorySnapshot | 15032313 | err | 11777 | Unknown result type: { } |  |
| Service.GetClients | 15032313 | err | 169 | Unable to cast object of type 'System.Collections.Generic.LinkedList`1[mt5rest.ClientStatus]' to type 'mt5rest.ClientStatus[]'. |  |
| Service.GetDemo | 15032313 | err | 0 | side-effect（开 demo 户）——skipped 如实缺 |  |
| Connection.CheckConnect | 15032313 | err | 0 | CheckConnect 需独立连接 id（Connect token 径不可复用）——skipped 如实缺 |  |
| Subscriptions.SubscribeOrderProfit | 15032313 | ok | 170 |  | subscribed |
| Subscriptions.SubscribeTickValue | 15032313 | ok | 168 |  | subscribed |
| Subscriptions.SubscribeOrderUpdate | 15032313 | ok | 168 |  | subscribed |
| Subscriptions.SubscribeMarketWatch | 15032313 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Subscriptions.SubscribeOpenedOrdersTickets | 15032313 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Streams.OnOrderUpdate | 15032313 | stream | 12010 | frames=0 (window 12s) | frames=0 |
| Streams.OnQuote | 15032313 | stream | 12001 | frames=0 (window 12s) | frames=0 |
| Streams.OnMail | 15032313 | stream | 172 | frames=0 (window 12s) | frames=0 |
| TickHistory.TickHistoryRequest | 15032313 | ok | 172 |  | ticket |
| TickHistory.OnTickHistory | 15032313 | stream | 4478 | frames=1 | frames=1 |
| TickHistory.TickHistoryStop | 15032313 | ok | 0 |  | stopped |
| Connection.ConnectEx | 15032313 | skipped | 0 | 连接变体（probe 走 Connect 主径）——未单独实测 |  |
| Connection.ConnectProxy | 15032313 | skipped | 0 | 代理连接变体——未单独实测 |  |
| Connection.Connect | 110660304 | ok | 258 |  | token |
| MT5.Account | 110660304 | ok | 168 |  | result |
| MT5.AccountSummary | 110660304 | ok | 170 |  | result |
| MT5.OpenedOrders | 110660304 | ok | 171 |  |  |
| MT5.OpenedOrdersTickets | 110660304 | ok | 168 |  |  |
| MT5.OrderHistory | 110660304 | ok | 269 |  |  |
| MT5.PendingOrderHistory | 110660304 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.OrderHistoryPagination | 110660304 | err | 177 | rpc error: code = Unimplemented desc =  |  |
| MT5.Symbols | 110660304 | ok | 268 |  | result |
| MT5.SymbolList | 110660304 | ok | 169 |  | result |
| MT5.GetQuote | 110660304 | err | 169 | Symbol XAUUSD not exist (:110660304) |  |
| MT5.GetQuoteMany | 110660304 | err | 168 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| MT5.MarketWatchMany | 110660304 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.SymbolParams | 110660304 | err | 169 | Symbol not found: XAUUSD (:110660304) |  |
| MT5.SymbolParamsMany | 110660304 | err | 169 | Symbol not found: XAUUSD (:110660304) |  |
| MT5.ServerTimezone | 110660304 | ok | 168 |  | result |
| MT5.IsTradeSession | 110660304 | err | 168 | The given key 'XAUUSD' was not present in the dictionary. (:110660304) |  |
| MT5.IsQuoteSession | 110660304 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:110660304) |  |
| MT5.GetTickValueMany | 110660304 | err | 182 | Symbol not found: XAUUSD (:110660304) |  |
| MT5.Mails | 110660304 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.RequiredMargin | 110660304 | err | 172 | Symbol XAUUSD not exist (:110660304) |  |
| QuoteHistory.PriceHistory | 110660304 | err | 169 | XAUUSD not exist (:110660304) |  |
| QuoteHistory.PriceHistoryToday | 110660304 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| Service.Ping | 110660304 | ok | 168 |  | result |
| Service.Health | 110660304 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Service.Version | 110660304 | ok | 173 |  | result |
| Service.Search | 110660304 | ok | 224 |  | result |
| Subscriptions.Subscribe | 110660304 | ok | 0 |  | symbol |
| Streams.Events | 110660304 | ok | 171 | frames=0 first= | frames=0 |
| MT5.IsTradeSessionMany | 110660304 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:110660304) |  |
| MT5.IsQuoteSessionMany | 110660304 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:110660304) |  |
| MT5.SymbolSessionsEx | 110660304 | err | 169 | XAUUSD not exist (:110660304) |  |
| MT5.SymbolSessionsExMany | 110660304 | err | 169 | System.String[] not exist (:110660304) |  |
| MT5.TickValueWithSize | 110660304 | err | 172 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| MT5.OpenedOrder | 110660304 | err | 168 | OK null response |  |
| Subscriptions.SubscribeMany | 110660304 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| Subscriptions.UnSubscribeMany | 110660304 | ok | 169 |  | result |
| Streams.OnTickValue | 110660304 | stream | 12013 | frames=0 | frames=0 |
| Streams.OnOrderProfit | 110660304 | stream | 12011 | frames=0 | frames=0 |
| Streams.OnMarketWatch | 110660304 | stream | 169 | frames=0 | frames=0 |
| Streams.OnOpenedOrdersTickets | 110660304 | stream | 169 | frames=0 | frames=0 |
| Connection.Disconnect | 110660304 | ok | 0 |  | session-closed |
| QuoteHistory.PriceHistoryMonth | 110660304 | err | 173 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| QuoteHistory.PriceHistoryMonthMany | 110660304 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| QuoteHistory.PriceHistoryTodayMany | 110660304 | err | 170 | One or more errors occurred. (XAUUSD not exist) (:110660304) |  |
| QuoteHistory.PriceHistoryMany | 110660304 | ok | 172 |  | result |
| QuoteHistory.PriceHistoryHighLow | 110660304 | err | 169 | XAUUSD not exist (:110660304) |  |
| QuoteHistory.PriceHistoryEx | 110660304 | err | 169 | XAUUSD not exist (:110660304) |  |
| QuoteHistory.PriceHistoryExMany | 110660304 | ok | 168 |  | result |
| Service.PingHost | 110660304 | ok | 174 |  | result |
| Service.PingHostMany | 110660304 | err | 168 | host.Length != port.Length (:0) |  |
| Service.MemorySnapshot | 110660304 | err | 21316 | Unknown result type: { } |  |
| Service.GetClients | 110660304 | err | 169 | Unable to cast object of type 'System.Collections.Generic.LinkedList`1[mt5rest.ClientStatus]' to type 'mt5rest.ClientStatus[]'. |  |
| Service.GetDemo | 110660304 | err | 0 | side-effect（开 demo 户）——skipped 如实缺 |  |
| Connection.CheckConnect | 110660304 | err | 0 | CheckConnect 需独立连接 id（Connect token 径不可复用）——skipped 如实缺 |  |
| Subscriptions.SubscribeOrderProfit | 110660304 | ok | 168 |  | subscribed |
| Subscriptions.SubscribeTickValue | 110660304 | ok | 169 |  | subscribed |
| Subscriptions.SubscribeOrderUpdate | 110660304 | ok | 173 |  | subscribed |
| Subscriptions.SubscribeMarketWatch | 110660304 | err | 171 | rpc error: code = Unimplemented desc =  |  |
| Subscriptions.SubscribeOpenedOrdersTickets | 110660304 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| Streams.OnOrderUpdate | 110660304 | stream | 12011 | frames=0 (window 12s) | frames=0 |
| Streams.OnQuote | 110660304 | stream | 12010 | frames=0 (window 12s) | frames=0 |
| Streams.OnMail | 110660304 | stream | 168 | frames=0 (window 12s) | frames=0 |
| TickHistory.TickHistoryRequest | 110660304 | ok | 168 |  | ticket |
| TickHistory.OnTickHistory | 110660304 | stream | 12011 | frames=0 | frames=0 |
| TickHistory.TickHistoryStop | 110660304 | ok | 0 |  | stopped |
| Connection.ConnectEx | 110660304 | skipped | 0 | 连接变体（probe 走 Connect 主径）——未单独实测 |  |
| Connection.ConnectProxy | 110660304 | skipped | 0 | 代理连接变体——未单独实测 |  |
| .MT5.ChangePassword |  | mutating-skipped | 0 | 真户禁跑（禁线）——未实测如实缺 |  |
| .Trading.OrderSend |  | mutating-skipped | 0 | 真户禁跑（禁线）——未实测如实缺 |  |
| .Trading.OrderModify |  | mutating-skipped | 0 | 真户禁跑（禁线）——未实测如实缺 |  |
| .Trading.OrderClose |  | mutating-skipped | 0 | 真户禁跑（禁线）——未实测如实缺 |  |
| Connection.Connect(demo) | 277842155 | ok | 2045 |  | endpoint=18.163.85.196:443 |
| Trading.OrderSend | 277842155 | ok | 754 |  | ticket=2701577885,volume=0.01,BTCUSDm |
| Trading.OrderModify | 277842155 | ok | 807 |  | ticket=2701577885 |
| Trading.OrderClose | 277842155 | ok | 696 |  | ticket=2701577885 |
| Trading.OrderSend(pending) | 277842155 | ok | 802 |  | ticket=2701577888,BuyLimit@1.0 |
| Trading.OrderModify(pending) | 277842155 | ok | 751 |  |  |
| Trading.OrderClose(pending delete) | 277842155 | ok | 465 |  |  |
| MT5.Account | 277842155 | ok | 168 |  | result |
| MT5.AccountSummary | 277842155 | ok | 168 |  | result |
| MT5.OpenedOrders | 277842155 | ok | 176 |  | result |
| MT5.OpenedOrdersTickets | 277842155 | ok | 170 |  | result |
| MT5.OrderHistory | 277842155 | ok | 670 |  | result |
| MT5.PendingOrderHistory | 277842155 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.OrderHistoryPagination | 277842155 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.Symbols | 277842155 | ok | 260 |  | result |
| MT5.SymbolList | 277842155 | ok | 182 |  | result |
| MT5.GetQuote | 277842155 | err | 171 | Symbol XAUUSD not exist (:277842155) |  |
| MT5.GetQuoteMany | 277842155 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:277842155) |  |
| MT5.MarketWatchMany | 277842155 | err | 168 | rpc error: code = Unimplemented desc =  |  |
| MT5.SymbolParams | 277842155 | err | 173 | Symbol not found: XAUUSD (:277842155) |  |
| MT5.SymbolParamsMany | 277842155 | err | 169 | Symbol not found: XAUUSD (:277842155) |  |
| MT5.ServerTimezone | 277842155 | ok | 168 |  |  |
| MT5.IsTradeSession | 277842155 | err | 169 | The given key 'XAUUSD' was not present in the dictionary. (:277842155) |  |
| MT5.IsQuoteSession | 277842155 | err | 170 | The given key 'XAUUSD' was not present in the dictionary. (:277842155) |  |
| MT5.GetTickValueMany | 277842155 | err | 169 | Symbol not found: XAUUSD (:277842155) |  |
| MT5.Mails | 277842155 | err | 174 | rpc error: code = Unimplemented desc =  |  |
| MT5.RequiredMargin | 277842155 | err | 169 | Symbol XAUUSD not exist (:277842155) |  |
| QuoteHistory.PriceHistory | 277842155 | err | 171 | XAUUSD not exist (:277842155) |  |
| QuoteHistory.PriceHistoryToday | 277842155 | err | 169 | One or more errors occurred. (XAUUSD not exist) (:277842155) |  |
| Service.Ping | 277842155 | ok | 168 |  | result |
| Service.Health | 277842155 | err | 172 | rpc error: code = Unimplemented desc =  |  |
| Service.Version | 277842155 | ok | 188 |  | result |
| Service.Search | 277842155 | ok | 223 |  | result |
| Subscriptions.Subscribe | 277842155 | ok | 0 |  | symbol |
| Streams.Events | 277842155 | ok | 169 | frames=0 first= | frames=0 |

## reflection 全量

- **mt5grpc.Connection**: Connect, ConnectEx, ConnectProxy, CheckConnect, Disconnect
- **mt5grpc.MT5**: Account, AccountSummary, OpenedOrders, OpenedOrder, OpenedOrdersTickets, OrderHistory, PendingOrderHistory, OrderHistoryPagination, Symbols, SymbolList, GetQuote, GetQuoteMany, MarketWatchMany, SymbolParams, SymbolParamsMany, SymbolSessionsEx, SymbolSessionsExMany, ServerTimezone, IsTradeSession, IsTradeSessionMany, IsQuoteSession, IsQuoteSessionMany, GetTickValueMany, TickValueWithSize, ChangePassword, Mails, RequiredMargin
- **mt5grpc.Trading**: OrderSend, OrderModify, OrderClose
- **mt5grpc.Streams**: Events, OnOrderUpdate, OnQuote, OnTickValue, OnOrderProfit, OnMarketWatch, OnTickHistory, OnMail, OnOpenedOrdersTickets
- **mt5grpc.TickHistory**: TickHistoryRequest, TickHistoryStop, OnTickHistory
- **mt5grpc.QuoteHistory**: PriceHistoryMonth, PriceHistoryMonthMany, PriceHistoryToday, PriceHistoryTodayMany, PriceHistory, PriceHistoryMany, PriceHistoryHighLow, PriceHistoryEx, PriceHistoryExMany
- **mt5grpc.Service**: Ping, Health, PingHost, PingHostMany, MemorySnapshot, Search, GetClients, GetDemo, Version
- **mt5grpc.Subscriptions**: Subscribe, SubscribeMany, UnSubscribe, UnSubscribeMany, SubscribeOrderProfit, SubscribeTickValue, SubscribeOrderUpdate, SubscribeMarketWatch, SubscribeOpenedOrdersTickets
- **grpc.reflection.v1alpha.ServerReflection**: ServerReflectionInfo

## 覆盖对账（reflection 全方法 vs 实测集）

缺 3 项：
- grpc.reflection.v1alpha.ServerReflection.ServerReflectionInfo
- mt5grpc.MT5.ChangePassword
- mt5grpc.Subscriptions.UnSubscribe
