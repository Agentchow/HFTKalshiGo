package ticker

// SoccerAliases maps alternate spellings to canonical team names.
// Ported from HFTKalshi/Executables/LIVEModeling/Soccer/team_name_aliases.py.
var SoccerAliases = map[string]string{
	// Premier League
	"man united": "manchester united", "man utd": "manchester united", "manchester utd": "manchester united",
	"man city": "manchester city", "manchester c": "manchester city",
	"wolves": "wolverhampton wanderers", "wolverhampton": "wolverhampton wanderers",
	"brighton": "brighton & hove albion", "brighton hove albion": "brighton & hove albion", "brighton and hove albion": "brighton & hove albion",
	"nottm forest": "nottingham forest", "nott'm forest": "nottingham forest", "nottingham": "nottingham forest",
	"spurs": "tottenham hotspur", "tottenham": "tottenham hotspur",
	"west ham":  "west ham united",
	"newcastle": "newcastle united", "newcastle utd": "newcastle united",
	"leicester":     "leicester city",
	"leeds":         "leeds united",
	"sheffield utd": "sheffield united", "sheffield": "sheffield united",
	"afc bournemouth": "bournemouth",

	// La Liga
	"granada cf":      "granada",
	"atletico madrid": "atletico de madrid", "atletico": "atletico de madrid", "atl. madrid": "atletico de madrid", "atl madrid": "atletico de madrid",
	"r. sociedad":     "real sociedad",
	"athletic bilbao": "athletic club", "athletic": "athletic club", "ath bilbao": "athletic club", "bilbao": "athletic club",
	"celta vigo": "celta de vigo", "celta": "celta de vigo",
	"rayo": "rayo vallecano", "vallecano": "rayo vallecano",
	"betis":                  "real betis",
	"alaves":                 "deportivo alaves",
	"deportivo de la coruna": "deportivo", "dep. la coruna": "deportivo",
	"cadiz cf": "cadiz", "burgos cf": "burgos",
	"racing santander": "santander", "real zaragoza": "zaragoza", "fc andorra": "andorra",

	// Bundesliga
	"bayern munich": "bayern munchen", "bayern": "bayern munchen", "fc bayern": "bayern munchen", "fc bayern munchen": "bayern munchen", "fc bayern munich": "bayern munchen",
	"dortmund": "borussia dortmund", "bvb": "borussia dortmund",
	"borussia m'gladbach": "borussia monchengladbach", "b. monchengladbach": "borussia monchengladbach", "gladbach": "borussia monchengladbach", "monchengladbach": "borussia monchengladbach",
	"m\u00b4gladbach": "borussia monchengladbach",
	"leverkusen":      "bayer leverkusen", "bayer 04": "bayer leverkusen",
	"rb leipzig": "rasenballsport leipzig", "leipzig": "rasenballsport leipzig",
	"wolfsburg": "vfl wolfsburg", "hoffenheim": "tsg hoffenheim",
	"mainz": "mainz 05", "freiburg": "sc freiburg", "augsburg": "fc augsburg",
	"koln": "fc koln", "cologne": "fc koln", "fc cologne": "fc koln",
	"frankfurt":    "eintracht frankfurt",
	"union berlin": "1. fc union berlin",
	"heidenheim":   "1. fc heidenheim 1846",
	"hamburg":      "hamburger sv", "hsv": "hamburger sv",
	"bremen":    "werder bremen",
	"st. pauli": "fc st. pauli", "st pauli": "fc st. pauli",

	// Serie A
	"inter": "inter milan", "internazionale": "inter milan", "inter milano": "inter milan",
	"ac milan": "milan", "a.c. milan": "milan",
	"juve":   "juventus",
	"napoli": "ssc napoli", "roma": "as roma", "lazio": "ss lazio",
	"fiorentina": "acf fiorentina", "atalanta": "atalanta bc",
	"torino": "torino fc", "bologna": "bologna fc",
	"hellas verona": "verona",
	"parma calcio":  "parma", "parma calcio 1913": "parma",
	"juve stabia": "stabia",
	"sudtirol":    "sudtirol bolzano", "fc sudtirol": "sudtirol bolzano",

	// Ligue 1
	"psg": "paris saint-germain", "paris saint germain": "paris saint-germain", "paris sg": "paris saint-germain", "paris": "paris saint-germain",
	"marseille": "olympique marseille", "om": "olympique marseille",
	"lyon": "olympique lyonnais", "ol": "olympique lyonnais",
	"monaco": "as monaco", "lille": "lille osc", "lens": "rc lens", "nice": "ogc nice",
	"rennes": "stade rennais", "strasbourg": "rc strasbourg", "strasbourg alsace": "rc strasbourg",
	"brest": "stade brest", "stade brestois": "stade brest",
	"toulouse": "toulouse fc",

	// MLS
	"la galaxy": "los angeles galaxy", "lafc": "los angeles fc", "la fc": "los angeles fc",
	"nycfc": "new york city fc", "nyc fc": "new york city fc",
	"nyrb": "new york red bulls", "ny red bulls": "new york red bulls",
	"atlanta": "atlanta united", "atlanta united fc": "atlanta united",
	"dc united": "d.c. united", "dc": "d.c. united",
	"inter miami": "inter miami cf", "miami": "inter miami cf",
	"columbus": "columbus crew", "portland": "portland timbers",
	"seattle": "seattle sounders", "seattle sounders fc": "seattle sounders",
	"nashville":         "nashville sc",
	"houston dynamo fc": "houston dynamo", "houston": "houston dynamo",
	"minnesota united fc": "minnesota united", "minnesota": "minnesota united",
	"cf montreal": "cf montreal", "montreal": "cf montreal",
	"chicago fire fc": "chicago fire", "chicago": "chicago fire",
	"orlando city sc": "orlando city", "orlando": "orlando city",
	"st. louis city sc": "st. louis city", "st. louis city": "st. louis city", "st louis city sc": "st. louis city", "st louis city": "st. louis city",
	"vancouver whitecaps fc": "vancouver whitecaps", "vancouver": "vancouver whitecaps",
	"san diego fc": "san diego",

	// Champions League / International
	"benfica": "sl benfica", "porto": "fc porto",
	"sporting": "sporting cp", "sporting lisbon": "sporting cp",
	"ajax": "afc ajax", "ajax amsterdam": "afc ajax",
	"psv": "psv eindhoven", "eindhoven": "psv eindhoven",
	"galatasaray": "galatasaray sk", "fenerbahce": "fenerbahce sk", "besiktas": "besiktas jk",
	"club brugge": "club brugge kv", "club bruges": "club brugge kv",
	"cercle brugge ksv": "cercle brugge",
	"olympiacos":        "olympiacos piraeus", "olympiakos": "olympiacos piraeus",
	"bodoe/glimt":  "bodo/glimt",
	"ludogorets":   "ludogorets razgrad",
	"ferencvarosi": "ferencvaros",
	"din. zagreb":  "dinamo zagreb", "fk dynamo kyiv": "dynamo kyiv",

	// Liga MX
	"club america": "america", "cf america": "america",
	"guadalajara": "chivas guadalajara", "chivas": "chivas guadalajara", "guadalajara chivas": "chivas guadalajara",
	"tigres": "tigres uanl", "pumas unam": "unam pumas",
	"club tijuana": "tijuana", "tijuana de caliente": "tijuana", "xolos": "tijuana",
	"mazatlan fc": "mazatlan", "club leon": "leon",
	"atl. san luis": "san luis", "atletico san luis": "san luis",

	// Colombian Liga
	"jaguares de cordoba": "jaguares", "jaguares fc": "jaguares",
	"independiente santa fe": "santa fe", "independ. santa fe": "santa fe", "ind santa fe": "santa fe",
	"independiente medellin": "ind. medellin",
	"atletico nacional":      "atl. nacional",
	"deportivo cali":         "dep. cali", "cali": "dep. cali",
	"america de cali": "america cali",
	"dep. pasto":      "pasto", "deportivo pasto": "pasto",
	"alianza fc":          "alianza fc valledupar",
	"junior barranquilla": "junior", "junior fc": "junior",
	"millonarios fc":           "millonarios",
	"deportes tolima":          "tolima",
	"llaneros fc":              "llaneros",
	"boyaca chico":             "chico fc",
	"aguilas doradas rionegro": "aguilas doradas",
	"union santa fe":           "union",

	// Argentine Primera
	"belgrano": "belgrano de cordoba", "instituto": "instituto cordoba",
	"atl. tucuman": "tucuman", "atletico tucuman": "tucuman",
	"ind. rivadavia":  "rivadavia",
	"independiente":   "independiente avellaneda",
	"san lorenzo":     "san lorenzo de almagro",
	"est. rio cuarto": "rio cuarto", "estudiantes rio cuarto": "rio cuarto",
	"racing club": "racing avellaneda", "racing": "racing avellaneda",
	"talleres":         "talleres cordoba",
	"rosario central":  "rosario",
	"estudiantes l.p.": "estudiantes la plata", "estudiantes": "estudiantes la plata",
	"sarmiento junin": "junin", "sarmiento": "junin",
	"gimnasia l.p.": "gimnasia la plata", "gimnasia mendoza": "mendoza",
	"newells old boys": "newell's old boys",
	"barracas central": "barracas",
	"dep. riestra":     "riestra", "argentinos jrs": "argentinos juniors",

	// Brasileirao
	"flamengo rj":   "flamengo",
	"rb bragantino": "bragantino", "red bull bragantino": "bragantino",
	"atl. paranaense": "paranaense", "athletico paranaense": "paranaense",
	"atl. mineiro": "atletico mineiro",
	"ec vitoria":   "vitoria",

	// Belgian Pro League
	"st. truiden": "st. truidense", "sint-truiden": "st. truidense",
	"royale union sg": "union gilloise", "union saint-gilloise": "union gilloise",
	"antwerp": "royal antwerp", "royal antwerp fc": "royal antwerp",
	"standard liege": "standard", "standard de liege": "standard",
	"kv mechelen":      "mechelen",
	"raal la louviere": "la louviere",
	"charleroi":        "royal charleroi", "sporting charleroi": "royal charleroi",
	"rsc anderlecht": "anderlecht",
	"zulte-waregem":  "zulte waregem",

	// Liga Portugal
	"santa clara": "santa clara azores",
	"afs":         "avs futebol sad", "avs": "avs futebol sad",
	"vitoria guimaraes": "guimaraes", "vit. guimaraes": "guimaraes",
	"sc braga": "braga", "sp. braga": "braga",
	"gil vicente":        "vicente barcelos",
	"estrela da amadora": "estrela amadora", "estrela": "estrela amadora",

	// AFC Champions League
	"al hilal": "al-hilal", "al ahli": "al-ahli", "al nassr": "al-nassr",
	"al ittihad": "al-ittihad", "al ain": "al-ain",
	"persepolis fc": "persepolis", "esteghlal fc": "esteghlal",
	"ulsan hyundai": "ulsan", "ulsan hd": "ulsan",
	"jeonbuk motors": "jeonbuk", "jeonbuk hyundai motors": "jeonbuk",
	"pohang steelers":     "pohang",
	"yokohama f. marinos": "yokohama f marinos",
	"urawa red diamonds":  "urawa reds",
	"kawasaki frontale":   "kawasaki", "frontale": "kawasaki",
	"vissel kobe fc": "kobe", "vissel kobe": "kobe", "vissel": "kobe",

	// Saudi Pro League
	"al okhdood": "al-akhdoud", "al taawon": "al-taawoun", "al fayha": "al-fayha",
	"al qadsiah": "al-qadsiah", "al-qadsiah el-khobar": "al-qadsiah",
	"al shabab": "al-shabab", "al-shabab riyadh": "al-shabab",
	"al hazem":      "al-hazm",
	"al ahli saudi": "al-ahli",
	"al ittifaq":    "al-ittifaq", "al riyadh": "al-riyadh",
	"al fateh": "al-fateh", "al khaleej": "al-khaleej", "al suqoor": "al-suqoor",

	// Eredivisie
	"fc volendam": "volendam", "rkav volendam": "volendam",
	"nac breda": "breda", "sparta rotterdam": "sparta",
	"az alkmaar": "alkmaar", "az": "alkmaar",
	"heracles": "heracles almelo", "go ahead eagles": "ga eagles",
	"twente": "enschede", "fc twente": "enschede",

	// Danish Superliga
	"fc copenhagen": "copenhagen", "fc kobenhavn": "copenhagen",
	"randers fc": "randers", "odense bk": "odense", "ob": "odense",
	"sonderjyske": "soenderjyske", "brondby": "broendby",

	// Croatian HNL
	"lok. zagreb": "lokomotiva", "lokomotiva zagreb": "lokomotiva",
	"hajduk split": "hajduk", "nk varazdin": "varazdin",

	// EFL Championship
	"oxford utd": "oxford united", "west brom": "west bromwich", "west bromwich albion": "west bromwich",
	"coventry city": "coventry", "sheffield wed": "sheffield wednesday",

	// Bundesliga 2
	"arminia bielefeld": "bielefeld", "nurnberg": "nuremberg", "1. fc nurnberg": "nuremberg", "fc nurnberg": "nuremberg",
	"karlsruher sc": "karlsruhe", "holstein kiel": "kiel", "dynamo dresden": "dresden",
	"hannover 96": "hannover", "hertha berlin": "hertha", "hertha bsc": "hertha",
	"preussen munster": "munster",

	// J-League
	"shimizu s-pulse": "shimizu", "cerezo osaka": "cerezo", "gamba osaka": "gamba",
	"sanfrecce hiroshima": "hiroshima", "sanfrecce": "hiroshima",
	"nagoya grampus": "nagoya", "kashima antlers": "kashima", "kashiwa reysol": "kashiwa",
	"v-varen nagasaki": "v-varen", "avispa fukuoka": "avispa",
	"fc tokyo":        "tokyo",
	"fagiano okayama": "okayama", "fagiano o": "okayama", "fagiano": "okayama",
	"mito hollyhock": "mito", "mito h": "mito",
	"jef united chiba": "chiba", "united chiba": "chiba",
	"kyoto sanga": "kyoto", "kyoto sanga fc": "kyoto",

	// A-League
	"brisbane roar": "brisbane", "central coast mariners": "central coast",
	"newcastle jets": "newcastle united",
	"macarthur fc":   "macarthur",
	"ws wanderers":   "western sydney wanderers", "western sydney": "western sydney wanderers",
	"adelaide united": "adelaide", "perth glory": "perth",
	"wellington phoenix": "wellington", "auckland fc": "auckland",

	// Turkish Super Lig
	"kocaelispor": "kocaeli", "goztepe izmir": "goztepe",

	// Polish Ekstraklasa
	"piast gliwice": "gliwice", "cracovia": "cracovia krakow",
	"legia": "legia warszawa", "legia warsaw": "legia warszawa",
	"gks katowice": "katowice", "zaglebie": "zaglebie lubin",
	"bruk-bet termalica nieciecza": "nieciecza", "termalica": "nieciecza",
	"rakow czestochowa": "czestochowa", "rakow": "czestochowa",
}
