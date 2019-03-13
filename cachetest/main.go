package main

/*

sorted by misses

tinylfu: 1.926582242s 10000000 total 2201654 misses (hit rate 77 %)
clockpro: 1.801118736s 10000000 total 2212491 misses (hit rate 77 %)
arc: 3.281438966s 10000000 total 2220057 misses (hit rate 77 %)
slru: 2.310323819s 10000000 total 2254240 misses (hit rate 77 %)
s4lru: 1.562688403s 10000000 total 2259672 misses (hit rate 77 %)
clock: 1.404290226s 10000000 total 2587426 misses (hit rate 74 %)
lru: 2.073626834s 10000000 total 2643691 misses (hit rate 73 %)
tworand: 1.60019126s 10000000 total 2736554 misses (hit rate 72 %)
random: 1.578992853s 10000000 total 2900695 misses (hit rate 70 %)

*/

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/allegro/bigcache"
	"github.com/coocood/freecache"
	"github.com/dgryski/go-arc"
	"github.com/dgryski/go-clockpro"
	"github.com/dgryski/go-s4lru"
	"github.com/dgryski/go-tinylfu"
	"github.com/dgryski/trifles/cachetest/clock"
	"github.com/dgryski/trifles/cachetest/randmark"
	"github.com/dgryski/trifles/cachetest/random"
	"github.com/dgryski/trifles/cachetest/slru"
	"github.com/dgryski/trifles/cachetest/tworand"
	"github.com/golang/groupcache/lru"
	"github.com/pkg/profile"
)

func main() {

	n := flag.Int("n", 1000, "cache size")
	alg := flag.String("alg", "", "algorithm")
	file := flag.String("f", "", "input file")
	bloom := flag.Bool("bloom", false, "use bloom doorkeeper")
	prob := flag.Float64("prob", math.NaN(), "use probkeeper")
	cpuprofile := flag.Bool("cpuprofile", false, "cpuprofile")
	memprofile := flag.Bool("memprofile", false, "memprofile")

	flag.Parse()

	if *alg == "" {
		log.Fatalln("no algorithm provided (-alg)")
	}

	if *cpuprofile {
		defer profile.Start(profile.CPUProfile).Stop()
	}

	if *memprofile {
		defer profile.Start(profile.MemProfile).Stop()
	}

	count := 0
	miss := 0

	t0 := time.Now()

	var f func(string) bool

	var bouncer doorkeeper = nullkeeper(0)

	if *bloom {
		bouncer = newBloomkeeper(*n)
	}

	if !math.IsNaN(*prob) {
		bouncer = newProbkeeper(float32(*prob))
	}

	switch *alg {

	case "freecache":

		// HDR (24) + Key (8) + Value (8) = 40
		cache := freecache.NewCache(*n * 40)
		f = func(s string) bool {
			bs := []byte(s)
			if i, err := cache.Get(bs); err == freecache.ErrNotFound {
				if bouncer.allow(s) {
					cache.Set(bs, bs, 0)
				}
				return true
			} else {
				if string(i) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "bigcache":

		// Entry size = key (8) + value (8) + header (18) = 34
		cache, err := bigcache.NewBigCache(bigcache.Config{
			Shards:             256,
			LifeWindow:         0,
			MaxEntriesInWindow: *n,
			MaxEntrySize:       34,
			HardMaxCacheSize:   *n * 34 / 1024 / 1024,
			Verbose:            false,
		})
		if err != nil {
			panic(err)
		}

		f = func(s string) bool {
			if i, err := cache.Get(s); err == bigcache.ErrEntryNotFound {
				if bouncer.allow(s) {
					cache.Set(s, []byte(s))
				}
				return true
			} else {
				if string(i) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "fastcache":

		// Entry size = key (8) + value (8) + header (4) = 16
		cache := fastcache.New(*n * 16)
		f = func(s string) bool {
			b := []byte(s)
			if i := cache.Get(nil, b); i == nil || len(i) == 0 {
				if bouncer.allow(s) {
					cache.Set(b, b)
				}
				return true
			} else {
				if string(i) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "arc":

		cache := arc.New(*n)

		f = func(s string) bool {

			var miss bool

			cache.Get(s, func() interface{} {
				miss = true
				return s
			})

			return miss
		}

	case "random":

		cache := random.New(*n)

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "mark":

		cache := randmark.New(*n)

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "tworand":

		cache := tworand.New(*n)

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "lru":

		cache := lru.New(*n)

		f = func(s string) bool {
			if v, ok := cache.Get(s); !ok {
				if bouncer.allow(s) {
					cache.Add(s, s)
				}
				return true
			} else {
				if v.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "tinylfu":

		cache := tinylfu.New(*n, *n*10)

		f = func(s string) bool {
			if v, ok := cache.Get(s); !ok {
				if bouncer.allow(s) {
					cache.Add(s, s)
				}
				return true
			} else {
				if v.(string) != s {
					panic("key != value")
				}
			}

			return false

		}

	case "clock":

		cache := clock.New(*n)

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "clockpro":

		cache := clockpro.New(*n)

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "slru":

		cache := slru.New(int(float64(*n)*0.2), int(float64(*n)*0.8))

		f = func(s string) bool {
			if i := cache.Get(s); i == nil {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if i.(string) != s {
					panic("key != value")
				}
			}

			return false
		}

	case "s4lru":

		cache := s4lru.New(*n)

		f = func(s string) bool {
			if v, ok := cache.Get(s); !ok {
				if bouncer.allow(s) {
					cache.Set(s, s)
				}
				return true
			} else {
				if v.(string) != s {
					panic("key != value")
				}
			}

			return false

		}

	default:
		log.Fatalln("unknown algorithm")
	}

	var inputFile = os.Stdin
	if *file != "" {
		var err error
		inputFile, err = os.Open(*file)
		if err != nil {
			log.Fatalln(err)
		}
		defer inputFile.Close()
	}

	in := bufio.NewScanner(inputFile)
	for in.Scan() {
		if f(in.Text()) {
			miss++
		}
		count++
	}

	fmt.Printf("%s: %s %d total %d misses (hit rate %d %%)\n", *alg, time.Since(t0), count, miss, int(100*(float64(count-miss)/float64(count))))
}

type doorkeeper interface {
	allow(s string) bool
}

type nullkeeper int

func (nullkeeper) allow(s string) bool {
	return true
}
