package main

// The studio and network walls are a fixed list: Seerr has no endpoint that lists studios or networks. The IDs are TMDB's, checked against
// Seerr's /studio/{id} and /network/{id} (v3.5.1); names are brand names and are not translated. logo is the TMDB logo path. A list entry
// that stops resolving only makes its page answer NOT_FOUND, so the list is data to be kept up to date, not logic.
type wallEntry struct {
	id         int
	name, logo string
}

var studioWall = []wallEntry{
	{2, "Walt Disney Pictures", "/wdrCwmRnLFJhEoH8GSfymY85KHT.png"},
	{3, "Pixar", "/1TjvGVDMYsj6JBxOAkUHpPEwLf7.png"},
	{420, "Marvel Studios", "/hUzeosd33nzE5MCNsZxCGEKTXaQ.png"},
	{1, "Lucasfilm Ltd.", "/tlVSws0RvvtPBwViUyOFAO0vcQS.png"},
	{174, "Warner Bros. Pictures", "/zhD3hhtKB5qyv7ZeL4uLpNxgMVU.png"},
	{12, "New Line Cinema", "/2ycs64eqV5rqKYHyQK0GVoKGvfX.png"},
	{33, "Universal Pictures", "/8lvHyhjr8oUKOOy2dKXoALWKdp0.png"},
	{4, "Paramount Pictures", "/jay6WcMgagAklUt7i9Euwj1pzTF.png"},
	{5, "Columbia Pictures", "/71BqEFAF4V3qjjMPCpLuyJFB9A.png"},
	{34, "Sony Pictures", "/xAb1o9HrSvKBo9mnXC8fJKDNu00.png"},
	{25, "20th Century Fox", "/nM2MfoMqzJQRiSynsDabOtFKetD.png"},
	{21, "Metro-Goldwyn-Mayer", "/usUnaYV6hQnlVAXP6r4HwrlLFPG.png"},
	{1632, "Lionsgate", "/cisLn1YAUuptXVBa0xjq7ST9cH0.png"},
	{41077, "A24", "/1ZXsGaFPgrgS6ZZGS37AqD5uU12.png"},
	{521, "DreamWorks Animation", "/3BPX5VGBov8SDqTV7wC1L1xShAS.png"},
	{923, "Legendary Pictures", "/5UQsZrfbfG2dYJbx8DxfoTr2Bvu.png"},
	{3172, "Blumhouse Productions", "/rzKluDcRkIwHZK2pHsiT667A2Kw.png"},
	{10342, "Studio Ghibli", "/uFuxPEZRUcBTEiYIxjHJq62Vr77.png"},
}

var networkWall = []wallEntry{
	{213, "Netflix", "/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"},
	{49, "HBO", "/tuomPhY2UtuPTqqFnKMVHvSb724.png"},
	{3186, "HBO Max", "/nmU0UMDJB3dRRQSTUqawzF2Od1a.png"},
	{2739, "Disney+", "/1edZOYAfoyZyZ3rklNSiUpXX30Q.png"},
	{2552, "Apple TV", "/bngHRFi794mnMq34gfVcm9nDxN1.png"},
	{1024, "Prime Video", "/w7HfLNm9CWwRmAMU58udl2L7We7.png"},
	{453, "Hulu", "/pqUTCleNUiTLAVlelGxUgWn1ELh.png"},
	{4330, "Paramount+", "/fi83B1oztoS47xxcemFdPMhIzK.png"},
	{3353, "Peacock", "/gIAcGTjKKr0KOHL5s4O36roJ8p7.png"},
	{174, "AMC", "/pmvRmATOCaDykE6JrVoeYxlFHw3.png"},
	{88, "FX", "/aexGjtcs42DgRtZh7zOxayiry4J.png"},
	{4, "BBC One", "/uJjcCg3O4DMEjM0xtno9OWFciRP.png"},
	{6, "NBC", "/cm111bsDVlYaC1foL0itvEI4yLG.png"},
	{16, "CBS", "/wju8KhOUsR5y4bH9p3Jc50hhaLO.png"},
	{2, "ABC", "/2uy2ZWcplrSObIyt4x0Y9rkG6qO.png"},
	{19, "FOX", "/1DSpHrWyOORkL9N2QHX7Adt31mQ.png"},
}
