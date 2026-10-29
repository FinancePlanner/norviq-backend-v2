package social

// achievementDef is the part of AchievementDefinition the profile showcase
// reads. A copy of AchievementCatalogue.all (Swift 37128c4), in order; the
// achievements feature keeps the full catalogue.
type achievementDef struct {
	Key           string
	IconKey       string
	IsPerformance bool
	TitleEN       string
	TitlePT       string
}

var achievementCatalogue = []achievementDef{
	{Key: "start.first_holding", IconKey: "chart.pie.fill", IsPerformance: false, TitleEN: "First Holding", TitlePT: "Primeira posição"},
	{Key: "start.first_budget", IconKey: "wallet.pass.fill", IsPerformance: false, TitleEN: "Budget Set", TitlePT: "Orçamento definido"},
	{Key: "start.first_goal", IconKey: "target", IsPerformance: false, TitleEN: "Goal Setter", TitlePT: "Com objetivos"},
	{Key: "start.onboarded", IconKey: "checkmark.seal.fill", IsPerformance: false, TitleEN: "All Set Up", TitlePT: "Tudo a postos"},
	{Key: "start.bank_linked", IconKey: "building.columns.fill", IsPerformance: false, TitleEN: "Connected", TitlePT: "Ligado"},
	{Key: "start.broker_linked", IconKey: "link", IsPerformance: false, TitleEN: "Broker Linked", TitlePT: "Corretora ligada"},
	{Key: "discipline.check_in", IconKey: "flame.fill", IsPerformance: false, TitleEN: "Showing Up", TitlePT: "Presença"},
	{Key: "discipline.check_in_year", IconKey: "calendar", IsPerformance: false, TitleEN: "Year of Presence", TitlePT: "Um ano de presença"},
	{Key: "discipline.budget_streak", IconKey: "chart.bar.fill", IsPerformance: false, TitleEN: "On Budget", TitlePT: "Dentro do orçamento"},
	{Key: "discipline.expense_days", IconKey: "list.bullet.clipboard.fill", IsPerformance: false, TitleEN: "Tracker", TitlePT: "Registo diário"},
	{Key: "discipline.recap_regular", IconKey: "doc.text.fill", IsPerformance: false, TitleEN: "Reflective", TitlePT: "Reflexivo"},
	{Key: "craft.thesis", IconKey: "pencil.and.outline", IsPerformance: false, TitleEN: "Thesis Writer", TitlePT: "Autor de teses"},
	{Key: "craft.memo_saved", IconKey: "bookmark.fill", IsPerformance: false, TitleEN: "Memo Keeper", TitlePT: "Guardião de notas"},
	{Key: "craft.watchlist_review", IconKey: "eye.fill", IsPerformance: false, TitleEN: "Reviewer", TitlePT: "Revisor"},
	{Key: "craft.target_hit", IconKey: "scope", IsPerformance: false, TitleEN: "On Target", TitlePT: "Na mouche"},
	{Key: "craft.position_sizing", IconKey: "ruler.fill", IsPerformance: false, TitleEN: "Sized Right", TitlePT: "Dimensão certa"},
	{Key: "craft.diversified", IconKey: "square.grid.2x2.fill", IsPerformance: false, TitleEN: "Diversified", TitlePT: "Diversificado"},
	{Key: "craft.rebalanced", IconKey: "arrow.triangle.2.circlepath", IsPerformance: false, TitleEN: "Rebalancer", TitlePT: "Reequilibrador"},
	{Key: "craft.patient_capital", IconKey: "hourglass", IsPerformance: false, TitleEN: "Patient Capital", TitlePT: "Capital paciente"},
	{Key: "plan.retirement", IconKey: "sunset.fill", IsPerformance: false, TitleEN: "Long View", TitlePT: "Visão de longo prazo"},
	{Key: "plan.scenario", IconKey: "slider.horizontal.3", IsPerformance: false, TitleEN: "What If", TitlePT: "E se..."},
	{Key: "plan.goal_done", IconKey: "flag.checkered", IsPerformance: false, TitleEN: "Goal Getter", TitlePT: "Cumpridor"},
	{Key: "plan.contributions", IconKey: "plus.circle.fill", IsPerformance: false, TitleEN: "Steady Saver", TitlePT: "Poupador constante"},
	{Key: "learn.articles", IconKey: "book.fill", IsPerformance: false, TitleEN: "Reader", TitlePT: "Leitor"},
	{Key: "learn.muse", IconKey: "sparkles", IsPerformance: false, TitleEN: "Curious", TitlePT: "Curioso"},
	{Key: "learn.mcp", IconKey: "terminal.fill", IsPerformance: false, TitleEN: "Power User", TitlePT: "Utilizador avançado"},
	{Key: "learn.pilot", IconKey: "airplane", IsPerformance: false, TitleEN: "Co-Pilot", TitlePT: "Copiloto"},
	{Key: "social.friends", IconKey: "person.2.fill", IsPerformance: false, TitleEN: "Friendly", TitlePT: "Amigável"},
	{Key: "social.first_post", IconKey: "text.bubble.fill", IsPerformance: false, TitleEN: "First Words", TitlePT: "Primeiras palavras"},
	{Key: "social.popular_post", IconKey: "hand.thumbsup.fill", IsPerformance: false, TitleEN: "Crowd Favourite", TitlePT: "Favorito da comunidade"},
	{Key: "social.group", IconKey: "person.3.fill", IsPerformance: false, TitleEN: "Part of Something", TitlePT: "Parte de algo"},
	{Key: "social.inviter", IconKey: "person.badge.plus", IsPerformance: false, TitleEN: "Recruiter", TitlePT: "Recrutador"},
	{Key: "perf.positive_year", IconKey: "arrow.up.right.circle.fill", IsPerformance: true, TitleEN: "Green Year", TitlePT: "Ano no verde"},
	{Key: "perf.beat_benchmark", IconKey: "trophy.fill", IsPerformance: true, TitleEN: "Market Beater", TitlePT: "Acima do mercado"},
	{Key: "perf.comeback", IconKey: "arrow.uturn.up.circle.fill", IsPerformance: true, TitleEN: "Comeback", TitlePT: "Recuperação"},
	{Key: "hidden.founding_member", IconKey: "star.circle.fill", IsPerformance: false, TitleEN: "Founding Member", TitlePT: "Membro fundador"},
}

var achievementByKey = func() map[string]achievementDef {
	m := make(map[string]achievementDef, len(achievementCatalogue))
	for _, d := range achievementCatalogue {
		m[d.Key] = d
	}
	return m
}()
