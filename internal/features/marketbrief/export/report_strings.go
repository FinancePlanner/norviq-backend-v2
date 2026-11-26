package export

// reportStrings are the report's labels and methodology text. Every string
// that also appears on the web page is copied verbatim from the web's
// edition strings (norviq-web-v2 src/lib/features/public/brief/edition/
// strings.ts), so the printed brief says exactly what the page says. Only
// index, changePct, inThisEdition, page, marketSessions and earningsToday
// are the report's own.
type reportStrings struct {
	marketIntelligence, published, marketDataAsOf, lisbonTime string
	edition, revision, limitedSources                         string
	marketsAtAGlance, glanceNote                              string
	marketPerformance, chartTitleClose, chartTitleOpen, time  string
	leadStory, developmentsClose, developmentsOpen, source    string
	acrossAssetClasses, instrument, level, change             string
	sourcesAndMethodology, citedSources, marketData           string
	marketDataMethod, howWritten, howWrittenBody              string
	degradedBody, notAvailable, legacyNote, disclaimer        string
	marketSessions, earningsToday                             string

	index, changePct, inThisEdition string
	page                            string // "Page %d of %d"
}

var reportEN = reportStrings{
	marketIntelligence:    "Market Intelligence",
	published:             "Published",
	marketDataAsOf:        "Market data as of",
	lisbonTime:            "(Lisbon time)",
	edition:               "Edition",
	revision:              "Revision",
	limitedSources:        "Limited sources",
	marketsAtAGlance:      "Markets at a glance",
	glanceNote:            "Moves from the previous close. Times are Lisbon time.",
	marketPerformance:     "Market performance",
	chartTitleClose:       "US session, from the previous close",
	chartTitleOpen:        "Overnight and early trading, from the previous close",
	time:                  "Time",
	leadStory:             "Lead story",
	developmentsClose:     "Market-moving developments",
	developmentsOpen:      "Also this morning",
	source:                "Source",
	acrossAssetClasses:    "Across asset classes",
	instrument:            "Instrument",
	level:                 "Level",
	change:                "Change",
	sourcesAndMethodology: "Sources and methodology",
	citedSources:          "Cited sources",
	marketData:            "Market data",
	marketDataMethod: "Index levels and moves are computed by Norviq from Yahoo Finance quotes at publication time. " +
		"Quotes may be delayed.",
	howWritten: "How this edition is written",
	howWrittenBody: "An AI model writes the text from those numbers and from recent news. A sentence that uses news " +
		"names its source. Levels, prices and percentages in a sentence without a source were checked against the " +
		"figures the model was given (market data, headlines and earnings), and sentences that failed the check were " +
		"removed. The check does not prove a sentence true, only that its figures were in the data.",
	degradedBody:   "Web sources were unavailable for this edition, so it was written from in-house data only.",
	marketSessions: "Market sessions today",
	earningsToday:  "Earnings today",
	notAvailable:   "Not available in this edition",
	legacyNote:     "This edition predates detailed market data, so it shows only the published levels.",
	disclaimer:     "AI-written from market data. Not financial advice.",

	index:         "Index",
	changePct:     "Change %",
	inThisEdition: "In this edition",
	page:          "Page %d of %d",
}

var reportPT = reportStrings{
	marketIntelligence:    "Market Intelligence",
	published:             "Publicado",
	marketDataAsOf:        "Dados de mercado às",
	lisbonTime:            "(hora de Lisboa)",
	edition:               "Edição",
	revision:              "Revisão",
	limitedSources:        "Fontes limitadas",
	marketsAtAGlance:      "Mercados num relance",
	glanceNote:            "Variações face ao fecho anterior. Horas de Lisboa.",
	marketPerformance:     "Desempenho do mercado",
	chartTitleClose:       "Sessão americana, face ao fecho anterior",
	chartTitleOpen:        "Negociação noturna e início da sessão, face ao fecho anterior",
	time:                  "Hora",
	leadStory:             "Destaque",
	developmentsClose:     "O que mexeu com os mercados",
	developmentsOpen:      "Também esta manhã",
	source:                "Fonte",
	acrossAssetClasses:    "Entre classes de ativos",
	instrument:            "Instrumento",
	level:                 "Nível",
	change:                "Variação",
	sourcesAndMethodology: "Fontes e metodologia",
	citedSources:          "Fontes citadas",
	marketData:            "Dados de mercado",
	marketDataMethod: "Os níveis e as variações dos índices são calculados pelo Norviq a partir das cotações da " +
		"Yahoo Finance no momento da publicação. As cotações podem ter atraso.", //nolint:misspell // Portuguese copy
	howWritten: "Como esta edição é escrita",
	howWrittenBody: "Um modelo de IA escreve o texto a partir desses números e de notícias recentes. Uma frase que " +
		"usa notícias indica a fonte. Níveis, preços e percentagens numa frase sem fonte foram verificados com os " + //nolint:misspell // Portuguese copy
		"números dados ao modelo (dados de mercado, títulos de notícias e resultados), e as frases que falharam a " +
		"verificação foram removidas. A verificação não prova que a frase é verdadeira, apenas que os seus números " +
		"estavam nos dados.",
	degradedBody:   "As fontes web não estavam disponíveis para esta edição, por isso foi escrita apenas com dados internos.",
	marketSessions: "Sessões de mercado hoje",
	earningsToday:  "Resultados hoje",
	notAvailable:   "Indisponível nesta edição",
	legacyNote:     "Esta edição é anterior aos dados de mercado detalhados, por isso mostra apenas os níveis publicados.",
	disclaimer:     "Escrito por IA a partir de dados de mercado. Não constitui aconselhamento financeiro.",

	index:         "Índice",
	changePct:     "Variação %",
	inThisEdition: "Nesta edição",
	page:          "Página %d de %d",
}

// reportStringsFor follows the web: pt-PT, and English for anything else.
func reportStringsFor(lang string) reportStrings {
	if lang == "pt-PT" {
		return reportPT
	}
	return reportEN
}
