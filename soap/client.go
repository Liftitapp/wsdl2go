package opti

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"

	"github.com/liftitapp/gomodels/core"
	"github.com/liftitapp/routeopt/grouper"
	"github.com/liftitapp/routeopt/routeopt"
	"github.com/liftitapp/routeopt/vehicleopt"
)

const wastePct = 0.1

// PosibleSolutions struct
type PosibleSolutions struct {
	Idx       int
	Solutions []SolutionItem
	Err       error
}

// SolutionItem what it is
type SolutionItem struct {
	ServiceType core.ServiceType
	Time        int
	Route       core.Route
}

// Solution gets a posible solution for all the zones
type Solution struct {
	// Per each area there is a set of solutions
	Iteration int
	Solution  map[int][]SolutionItem
}

// SolutionError error when solving the problen
type SolutionError struct {
	Pending map[int]core.Route
}

func (s SolutionError) Error() string {
	return "All solutions were not found"
}

// ImpossibleSolutionError when it's impossible to get a solution
type ImpossibleSolutionError struct {
	Tasks core.Route
}

func (s ImpossibleSolutionError) Error() string {
	return "Solutions could not be found for these routes"
}

// OptimizationParams params
type OptimizationParams struct {
	MaxTime    float64
	BaseClient *http.Client
}

// ErrEmptyRoute when there are one or less tasks
var ErrEmptyRoute = errors.New("The route must have more than 1 task")

// ErrEmptyServiceTypes there must be more than 0 services
var ErrEmptyServiceTypes = errors.New("There must be more than 0 service types")

// ErrInvalidMaxTime the max time
var ErrInvalidMaxTime = errors.New("The max time is invalid")

// DoOptimizations makes the respective optimizations
func DoOptimizations(route core.Route, sts []core.ServiceType,
	do func(Solution), params OptimizationParams) error {
	fmt.Println("ENTRO A OPTI")
	if len(route) <= 2 {
		return ErrEmptyRoute
	}
	if len(sts) == 0 {
		return ErrEmptyServiceTypes
	}
	if params.MaxTime < 0 {
		return ErrInvalidMaxTime
	}
	if params.BaseClient == nil {
		params.BaseClient = &http.Client{}
	}
	firstTask := route[0]
	routes := []core.Route{route[1:]}
	impossibleRoutes := core.Route{}
	i := 0
	solFound := 0
	for {
		if len(routes) == 0 {
			break
		}
		unsolvedRoutes := []core.Route{}
		for _, route := range routes {
			solution, err := solveRoute(firstTask, route, sts, params)
			switch v := err.(type) {
			case SolutionError:
				if solution != nil {
					solution.Iteration = i
					solFound += len(solution.Solution)
					do(*solution)

				}
				for _, r := range v.Pending {
					fmt.Printf("PENDING with len %d\n", len(r))
					if len(r) <= 1 {
						impossibleRoutes = append(impossibleRoutes, r...)
						continue
					}
					unsolvedRoutes = append(unsolvedRoutes, r)
				}
			case error:
				return v
			default:
			}
		}
		fmt.Printf("-----UNSOLVED ITER(%d) = %d\n", i, len(unsolvedRoutes))
		routes = unsolvedRoutes
		i++
	}
	fmt.Printf("All found solutions (%d)\n Impossible (%d)\n", solFound, len(impossibleRoutes))

	if len(impossibleRoutes) > 0 {
		impossibleRoutes = append(core.Route{firstTask}, impossibleRoutes...)
		return ImpossibleSolutionError{Tasks: impossibleRoutes}
	}

	return nil
}

func solveRoute(firstTask core.Task, route core.Route, sts []core.ServiceType, params OptimizationParams) (*Solution, error) {
	subgroups := grouper.Group(route)
	solChan := make(chan PosibleSolutions, 0)
	wg := sync.WaitGroup{}
	for i, subgroup := range subgroups {
		posGroupings, err := vehicleopt.OptimizeVehicles(
			subgroup, sts, wastePct)
		if err != nil {
			fmt.Printf("Error optimizing vehicles %s", err)
			return nil, err
		}
		fmt.Printf("Posible GROUPS %d\n", len(posGroupings))
		for _, group := range posGroupings {
			fmt.Printf("Validating grouping with %d groups\n",
				len(group))
			wg.Add(1)
			go validatePosibleSolution(i, firstTask, group,
				solChan, &wg, params)
		}
	}
	go wgWatcherSol(&wg, solChan)
	fmt.Println("ENTRO A WAIT CHAN")
	posibleSolutions := make(map[int][]PosibleSolutions)
	for sol := range solChan {
		// if sol.Err != nil {
		// 	continue
		// }
		if posibleSolutions[sol.Idx] == nil {
			posibleSolutions[sol.Idx] = make(
				[]PosibleSolutions, 0)
		}
		posibleSolutions[sol.Idx] = append(
			posibleSolutions[sol.Idx], sol)
	}
	fmt.Printf("nRoutes = %d\n", len(subgroups))
	//choose solution
	return consolidateSolution(subgroups, posibleSolutions)
	// if solution.Err != nil {
	// 	// fmt.Printf("Solution error %v", solution)
	// 	for i, s := range solgution.Pending {
	// 		fmt.Printf("Pending for subroute %d\n", i)
	// 		for _, t := range s {
	// 			fmt.Printf("- %s (%f, %f)", t.Address,
	// 				t.Lat, t.Lon)
	// 		}
	// 		fmt.Println("")
	// 	}
	// 	return solution.Err
	// }
}

/* consolidateSolution chooses a posible solution for each zone with
a random criteria
*/
func consolidateSolution(routes []core.Route, posi map[int][]PosibleSolutions) (*Solution, error) {
	sol := map[int][]SolutionItem{}
	for k, v := range posi {
		idx := rand.Intn(len(v))
		sol[k] = v[idx].Solutions
	}
	fmt.Printf("len routes %d len sol %d\n", len(routes), len(sol))
	if len(routes) == len(sol) {
		return &Solution{Solution: sol}, nil
	}

	unsol := map[int]core.Route{}
	for i := range routes {
		if _, ok := sol[i]; !ok {
			unsol[i] = routes[i]
		}
	}
	fmt.Printf("UNSOL (%d)\n", len(unsol))
	return &Solution{Solution: sol}, SolutionError{Pending: unsol}
}

/* validatePosibleSolution validates that a set of tasks of a given subregion
of a possible subgroup can get a solution optimized that fit the time
window
*/
func validatePosibleSolution(idx int, firstTask core.Task, group []vehicleopt.PosibleGrouping, solChan chan PosibleSolutions, solWg *sync.WaitGroup, params OptimizationParams) {
	doneChan := make(chan routeopt.PosibleOptimization, 0)
	wg := sync.WaitGroup{}
	for _, posGroup := range group {
		wg.Add(1)
		opt := routeopt.NewOptimizer(
			params.BaseClient,
			append([]core.Task{firstTask}, posGroup.Grouping...),
			posGroup.ServiceType,
			params.MaxTime,
			doneChan,
			&wg)
		go opt.OptmizeRoutePar()
	}
	go wgWatcherDone(&wg, doneChan)
	fmt.Println("ENTRO A WAIT CHAN")
	optimizedSolution := make([]SolutionItem, 0)
	for posOptimization := range doneChan {
		if posOptimization.Err != nil {
			// discards all this group
			// solChan <- PosibleSolutions{
			// 	Idx: idx,
			// 	Err: posOptimization.Err,
			// }
			solWg.Done()
			return
		}
		optimizedSolution = append(optimizedSolution, SolutionItem{
			Route:       posOptimization.Route,
			ServiceType: posOptimization.ServiceType,
			Time:        posOptimization.Time,
		})
	}
	solChan <- PosibleSolutions{
		Idx:       idx,
		Solutions: optimizedSolution,
	}
	solWg.Done()
}

func wgWatcherDone(wg *sync.WaitGroup,
	doneChan chan routeopt.PosibleOptimization) {
	wg.Wait()
	close(doneChan)
}

func wgWatcherSol(wg *sync.WaitGroup,
	solChan chan PosibleSolutions) {
	wg.Wait()
	close(solChan)
}
