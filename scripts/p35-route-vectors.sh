#!/usr/bin/env bash
# Run from apps/server through test-remote.sh. Generate vectors with the actual
# frozen Go RoutePointsCanonical implementation, not a second protocol.
set -euo pipefail
directory="$(mktemp -d ./.p35-vectors-XXXXXX)"
trap 'rm -rf -- "$directory"' EXIT
cat > "$directory/main.go" <<'GO'
package main
import (
 "encoding/json"
 "encoding/hex"
 "fmt"
 "math"
 "math/rand/v2"
 "rai/server/internal/syncing"
)
func main() {
 random := rand.New(rand.NewPCG(35, 20261027))
 cases := []any{}
 values := []float64{0, math.Copysign(0,-1), 1e-7, 1e-6, math.SmallestNonzeroFloat64, math.Nextafter(1,2), 31.2304, -81.4737}
 for i:=0;i<100;i++ {values=append(values,random.Float64()*180-90)}
 chain := syncing.EmptyRouteManifest()
 for i,value:=range values {
  altitude:=1e20; if i%2==1 {altitude=1e21}
  point:=syncing.RoutePoint{Sequence:int64(i),RecordedAtUs:1793059200000000+int64(i),Latitude:value,Longitude:121.4,Altitude:&altitude}
  canonical,digest:=syncing.RoutePointsCanonical([]syncing.RoutePoint{point})
  chain=syncing.AdvanceRouteManifest(chain,digest)
  cases=append(cases,map[string]any{"points":[]syncing.RoutePoint{point},"canonical":string(canonical),"sha256":hex.EncodeToString(digest),"manifest":hex.EncodeToString(chain)})
 }
 result,_:=json.Marshal(map[string]any{"source":"rai/server/internal/syncing.RoutePointsCanonical; synthetic","cases":cases})
 fmt.Printf("P35_ROUTE_VECTORS=%s\n",result)
}
GO
go run "$directory"
