package achievements

import (
	"strings"

	"norviq-api/internal/features/badges"
)

// Category is AchievementCategory.
type Category string

// AchievementCategory.allCases, in order.
const (
	CatGettingStarted Category = "getting_started"
	CatDiscipline     Category = "discipline"
	CatCraft          Category = "craft"
	CatPlanning       Category = "planning"
	CatLearning       Category = "learning"
	CatCommunity      Category = "community"
	CatPerformance    Category = "performance"
	CatHidden         Category = "hidden"
	CatClassic        Category = "classic"
)

var allCategories = []Category{
	CatGettingStarted, CatDiscipline, CatCraft, CatPlanning, CatLearning,
	CatCommunity, CatPerformance, CatHidden, CatClassic,
}

// Title is AchievementCategory.title(_:).
func (c Category) Title(lang Language) string {
	pt := lang == LangPT
	pick := func(p, e string) string {
		if pt {
			return p
		}
		return e
	}
	switch c {
	case CatGettingStarted:
		return pick("Primeiros passos", "Getting started")
	case CatDiscipline:
		return pick("Disciplina", "Discipline")
	case CatCraft:
		return pick("Ofício", "Craft")
	case CatPlanning:
		return pick("Planeamento", "Planning")
	case CatLearning:
		return pick("Aprendizagem", "Learning")
	case CatCommunity:
		return pick("Comunidade", "Community")
	case CatPerformance:
		return pick("Desempenho", "Performance")
	case CatHidden:
		return pick("Segredos", "Secrets")
	}
	return pick("Clássicos", "Classic")
}

// Language is AchievementLanguage.
type Language int

const (
	LangEN Language = iota
	LangPT
)

// LanguageFrom is AchievementLanguage.from(acceptLanguage:): Portuguese when
// the first language tag starts with "pt", English otherwise. Swift's
// split(separator:) drops empty pieces, so ",pt" is Portuguese.
func LanguageFrom(acceptLanguage string) Language {
	for _, part := range strings.Split(acceptLanguage, ",") {
		if part == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part)), "pt") {
			return LangPT
		}
		return LangEN
	}
	return LangEN
}

// TierSpec is AchievementTierSpec.
type TierSpec struct {
	Tier      string
	Threshold int
	XP        int
}

type copyText struct{ Title, Description string }

// Definition is AchievementDefinition.
type Definition struct {
	Key      string
	Category Category
	IconKey  string
	// Ascending thresholds; a single-tier achievement has one spec with tier "".
	Tiers []TierSpec
	// Evaluated only by the sweep, never live.
	SweepOnly     bool
	IsPerformance bool
	IsHidden      bool
	EN, PT        copyText
}

// Copy is definition.copy[language] ?? copy[.en]: both always exist.
func (d *Definition) Copy(lang Language) copyText {
	if lang == LangPT {
		return d.PT
	}
	return d.EN
}

func (d *Definition) tierSpec(tier string) (TierSpec, bool) {
	for _, s := range d.Tiers {
		if s.Tier == tier {
			return s, true
		}
	}
	return TierSpec{}, false
}

// xpFor is definition.tiers.first { $0.tier == tier }?.xp ?? 0.
func (d *Definition) xpFor(tier string) int {
	s, _ := d.tierSpec(tier)
	return s.XP
}

var tierNames = []string{"bronze", "silver", "gold"}

func tiered(thresholds ...int) []TierSpec {
	xp := []int{25, 50, 100}
	out := make([]TierSpec, len(thresholds))
	for i, t := range thresholds {
		out[i] = TierSpec{Tier: tierNames[i], Threshold: t, XP: xp[i]}
	}
	return out
}

func single(threshold, xp int) []TierSpec {
	return []TierSpec{{Tier: "", Threshold: threshold, XP: xp}}
}

type defFlags struct{ sweepOnly, performance, hidden bool }

func def(key string, cat Category, icon string, tiers []TierSpec, f defFlags, en, pt [2]string) Definition {
	return Definition{
		Key: key, Category: cat, IconKey: icon, Tiers: tiers,
		SweepOnly: f.sweepOnly, IsPerformance: f.performance, IsHidden: f.hidden,
		EN: copyText{en[0], en[1]}, PT: copyText{pt[0], pt[1]},
	}
}

var (
	plain     = defFlags{}
	sweepOnly = defFlags{sweepOnly: true}
	perf      = defFlags{sweepOnly: true, performance: true}
	hidden    = defFlags{hidden: true}
)

// Catalogue is AchievementCatalogue.all, in order.
var Catalogue = []Definition{
	def("start.first_holding", CatGettingStarted, "chart.pie.fill", single(1, 25), plain,
		[2]string{"First Holding", "Add your first holding."}, [2]string{"Primeira posição", "Adiciona a tua primeira posição."}),
	def("start.first_budget", CatGettingStarted, "wallet.pass.fill", single(1, 25), plain,
		[2]string{"Budget Set", "Set up your first budget."}, [2]string{"Orçamento definido", "Define o teu primeiro orçamento."}),
	def("start.first_goal", CatGettingStarted, "target", single(1, 25), plain,
		[2]string{"Goal Setter", "Create your first goal."}, [2]string{"Com objetivos", "Cria o teu primeiro objetivo."}),
	def("start.onboarded", CatGettingStarted, "checkmark.seal.fill", single(1, 25), plain,
		[2]string{"All Set Up", "Finish setting up your account."}, [2]string{"Tudo a postos", "Conclui a configuração da tua conta."}),
	def("start.bank_linked", CatGettingStarted, "building.columns.fill", single(1, 25), plain,
		[2]string{"Connected", "Link a bank account."}, [2]string{"Ligado", "Liga uma conta bancária."}),
	def("start.broker_linked", CatGettingStarted, "link", single(1, 25), plain,
		[2]string{"Broker Linked", "Link a broker account."}, [2]string{"Corretora ligada", "Liga uma conta de corretora."}),
	def("discipline.check_in", CatDiscipline, "flame.fill", tiered(7, 30, 100), plain,
		[2]string{"Showing Up", "Check in on consecutive days."}, [2]string{"Presença", "Faz check-in em dias seguidos."}),
	def("discipline.check_in_year", CatDiscipline, "calendar", single(365, 25), plain,
		[2]string{"Year of Presence", "Keep a 365-day check-in streak."}, [2]string{"Um ano de presença", "Mantém uma sequência de 365 dias de check-in."}),
	def("discipline.budget_streak", CatDiscipline, "chart.bar.fill", tiered(3, 6, 12), plain,
		[2]string{"On Budget", "Stay on budget for months in a row."}, [2]string{"Dentro do orçamento", "Cumpre o orçamento durante meses seguidos."}),
	def("discipline.expense_days", CatDiscipline, "list.bullet.clipboard.fill", tiered(30, 100, 365), plain,
		[2]string{"Tracker", "Log expenses on different days."}, [2]string{"Registo diário", "Regista despesas em dias diferentes."}),
	def("discipline.recap_regular", CatDiscipline, "doc.text.fill", single(3, 25), plain,
		[2]string{"Reflective", "Open your monthly recap three months in a row."}, [2]string{"Reflexivo", "Abre o teu resumo mensal três meses seguidos."}),
	def("craft.thesis", CatCraft, "pencil.and.outline", tiered(1, 10, 50), plain,
		[2]string{"Thesis Writer", "Write down your investment thesis."}, [2]string{"Autor de teses", "Escreve a tua tese de investimento."}),
	def("craft.memo_saved", CatCraft, "bookmark.fill", single(1, 25), plain,
		[2]string{"Memo Keeper", "Bookmark a position memo."}, [2]string{"Guardião de notas", "Guarda uma nota de posição nos favoritos."}),
	def("craft.watchlist_review", CatCraft, "eye.fill", tiered(1, 10, 50), plain,
		[2]string{"Reviewer", "Review items on your watchlist."}, [2]string{"Revisor", "Revê itens da tua lista de observação."}),
	def("craft.target_hit", CatCraft, "scope", tiered(1, 5, 20), plain,
		[2]string{"On Target", "Have a price target alert trigger."}, [2]string{"Na mouche", "Vê um alerta de preço-alvo ser ativado."}),
	def("craft.position_sizing", CatCraft, "ruler.fill", single(1, 25), plain,
		[2]string{"Sized Right", "Size a position with the terminal."}, [2]string{"Dimensão certa", "Dimensiona uma posição com o terminal."}),
	def("craft.diversified", CatCraft, "square.grid.2x2.fill", single(5, 25), sweepOnly,
		[2]string{"Diversified", "Hold stocks in five different sectors."}, [2]string{"Diversificado", "Tem ações em cinco setores diferentes."}),
	def("craft.rebalanced", CatCraft, "arrow.triangle.2.circlepath", single(1, 25), plain,
		[2]string{"Rebalancer", "Complete a rebalance plan."}, [2]string{"Reequilibrador", "Conclui um plano de reequilíbrio."}),
	def("craft.patient_capital", CatCraft, "hourglass", single(1, 25), sweepOnly,
		[2]string{"Patient Capital", "Hold a stock for over a year."}, [2]string{"Capital paciente", "Mantém uma ação durante mais de um ano."}),
	def("plan.retirement", CatPlanning, "sunset.fill", single(1, 25), plain,
		[2]string{"Long View", "Create a retirement plan."}, [2]string{"Visão de longo prazo", "Cria um plano de reforma."}),
	def("plan.scenario", CatPlanning, "slider.horizontal.3", tiered(1, 10, 50), plain,
		[2]string{"What If", "Run what-if scenarios."}, [2]string{"E se...", "Corre cenários hipotéticos."}),
	def("plan.goal_done", CatPlanning, "flag.checkered", tiered(1, 3, 10), plain,
		[2]string{"Goal Getter", "Complete your goals."}, [2]string{"Cumpridor", "Conclui os teus objetivos."}),
	def("plan.contributions", CatPlanning, "plus.circle.fill", tiered(10, 50, 100), plain,
		[2]string{"Steady Saver", "Add contributions to your goals."}, [2]string{"Poupador constante", "Faz contribuições para os teus objetivos."}),
	def("learn.articles", CatLearning, "book.fill", tiered(5, 25, 100), plain,
		[2]string{"Reader", "Read articles."}, [2]string{"Leitor", "Lê artigos."}),
	def("learn.muse", CatLearning, "sparkles", tiered(1, 10, 50), plain,
		[2]string{"Curious", "Chat with the AI assistant."}, [2]string{"Curioso", "Conversa com o assistente de IA."}),
	def("learn.mcp", CatLearning, "terminal.fill", single(1, 25), plain,
		[2]string{"Power User", "Connect an access token or app."}, [2]string{"Utilizador avançado", "Liga um token de acesso ou uma aplicação."}),
	def("learn.pilot", CatLearning, "airplane", single(1, 25), plain,
		[2]string{"Co-Pilot", "Follow a pilot."}, [2]string{"Copiloto", "Segue um piloto."}),
	def("social.friends", CatCommunity, "person.2.fill", tiered(1, 5, 20), plain,
		[2]string{"Friendly", "Make friends on Norviq."}, [2]string{"Amigável", "Faz amigos no Norviq."}),
	def("social.first_post", CatCommunity, "text.bubble.fill", single(1, 25), plain,
		[2]string{"First Words", "Write your first board post."}, [2]string{"Primeiras palavras", "Escreve a tua primeira publicação."}),
	def("social.popular_post", CatCommunity, "hand.thumbsup.fill", single(10, 25), plain,
		[2]string{"Crowd Favourite", "Get a post to a score of 10."}, [2]string{"Favorito da comunidade", "Leva uma publicação a uma pontuação de 10."}),
	def("social.group", CatCommunity, "person.3.fill", single(1, 25), plain,
		[2]string{"Part of Something", "Join a group."}, [2]string{"Parte de algo", "Junta-te a um grupo."}),
	def("social.inviter", CatCommunity, "person.badge.plus", tiered(1, 3, 10), plain,
		[2]string{"Recruiter", "Bring friends to Norviq."}, [2]string{"Recrutador", "Traz amigos para o Norviq."}),
	def("perf.positive_year", CatPerformance, "arrow.up.right.circle.fill", single(1, 100), perf,
		[2]string{"Green Year", "Finish a calendar year in the green."}, [2]string{"Ano no verde", "Termina um ano civil com ganhos."}),
	def("perf.beat_benchmark", CatPerformance, "trophy.fill", single(1, 100), perf,
		[2]string{"Market Beater", "Beat the benchmark over a year."}, [2]string{"Acima do mercado", "Supera o índice de referência ao longo de um ano."}),
	def("perf.comeback", CatPerformance, "arrow.uturn.up.circle.fill", single(1, 100), perf,
		[2]string{"Comeback", "Recover from a drawdown."}, [2]string{"Recuperação", "Recupera de uma queda."}),
	def("hidden.founding_member", CatHidden, "star.circle.fill", single(1, 50), hidden,
		[2]string{"Founding Member", "You were one of the first 1,000 members."}, [2]string{"Membro fundador", "Foste um dos primeiros 1000 membros."}),
}

var byKey = func() map[string]*Definition {
	m := make(map[string]*Definition, len(Catalogue))
	for i := range Catalogue {
		m[Catalogue[i].Key] = &Catalogue[i]
	}
	return m
}()

// Lookup is AchievementCatalogue.definition(_:): catalogue keys only.
func Lookup(key string) *Definition { return byKey[key] }

// legacyPT is AchievementCatalogue.legacyPT.
var legacyPT = map[string][2]string{
	"first_purchase":  {"Primeira compra", "Compra a tua primeira ação"},
	"news_reader":     {"Leitor de notícias", "Mantém-te informado lendo notícias de ações"},
	"investor":        {"Investidor", "Constrói a tua carteira com investimentos regulares"},
	"saver":           {"Poupador", "Mantém uma taxa de poupança positiva todos os meses"},
	"frugal_fun":      {"Diversão frugal", "Fica abaixo do orçamento de lazer"},
	"spending_detox":  {"Detox de gastos", "Passa dias seguidos sem gastar"},
	"growth_mindset":  {"Mentalidade de crescimento", "Poupa mais dinheiro de mês para mês"},
}

// legacyDefinitions is BadgeType.allCases.map(legacyDefinition(for:)).
var legacyDefinitions = func() []Definition {
	xp := []int{25, 50, 100} // XPRules.badge(bronze/silver/gold)
	out := make([]Definition, 0, len(badges.Definitions))
	for _, b := range badges.Definitions {
		specs := make([]TierSpec, 3)
		for i := range 3 {
			specs[i] = TierSpec{Tier: tierNames[i], Threshold: b.Thresholds[i], XP: xp[i]}
		}
		pt, ok := legacyPT[string(b.Type)]
		if !ok {
			pt = [2]string{b.Title, b.Description}
		}
		out = append(out, Definition{
			Key: "legacy." + string(b.Type), Category: CatClassic, IconKey: b.IconName, Tiers: specs,
			EN: copyText{b.Title, b.Description}, PT: copyText{pt[0], pt[1]},
		})
	}
	return out
}()

// lookupWithLegacy is the definition for a stored key, including
// `legacy.<BadgeType>` keys.
func lookupWithLegacy(key string) *Definition {
	if d := Lookup(key); d != nil {
		return d
	}
	if !strings.HasPrefix(key, "legacy.") {
		return nil
	}
	for i := range legacyDefinitions {
		if legacyDefinitions[i].Key == key {
			return &legacyDefinitions[i]
		}
	}
	return nil
}

// performanceKeys is AchievementCatalogue.all.filter(\.isPerformance).
func performanceKeys() []string {
	var out []string
	for _, d := range Catalogue {
		if d.IsPerformance {
			out = append(out, d.Key)
		}
	}
	return out
}

func catalogueKeys() []string {
	out := make([]string, len(Catalogue))
	for i, d := range Catalogue {
		out[i] = d.Key
	}
	return out
}

// capitalized is Swift's String.capitalized for the tier names (one
// lowercase word each).
func capitalized(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
