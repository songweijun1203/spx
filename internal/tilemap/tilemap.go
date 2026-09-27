/*
 * Copyright (c) 2021 The XGo Authors (xgo.dev). All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package tilemap

import (
	"path"
	"slices"
	"strings"

	"github.com/goplus/spbase/mathf"
)

type vec2i struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`
}

// Vec2 表示世界坐标系中的二维坐标（单位：像素）。
type Vec2 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// tileSize 表示瓦片尺寸。
type tileSize struct {
	Width  int32 `json:"width"`
	Height int32 `json:"height"`
}

// physicsData 保存瓦片的物理属性。
type physicsData struct {
	CollisionPoints []Vec2 `json:"collision_points,omitempty"` // 碰撞多边形的顶点坐标。
}

// tileInfo 表示瓦片集中的单个瓦片信息。
type tileInfo struct {
	AtlasCoords vec2i       `json:"atlas_coords"`
	Physics     physicsData `json:"physics,omitempty"`
}

// tileSource 表示一个瓦片集资源。
type tileSource struct {
	ID          int32      `json:"id"`
	TexturePath string     `json:"texture_path"`
	Tiles       []tileInfo `json:"tiles"`
}

// tileSet 表示完整的瓦片集信息。
type tileSet struct {
	Sources []tileSource `json:"sources"`
}

// tileInstance 表示地图中放置的瓦片实例。
type tileInstance struct {
	TileCoords  vec2i `json:"tile_coords"`
	SourceID    int32 `json:"source_id"`
	AtlasCoords vec2i `json:"atlas_coords"`
}

// tilemapLayer 表示使用紧凑瓦片数据格式的地图层。
type tilemapLayer struct {
	ID       int32   `json:"id"`
	Name     string  `json:"name"`
	ZIndex   int     `json:"z_index"`
	TileData []int32 `json:"tile_data"`
}

// tileMapData 表示完整的地图数据。
type tileMapData struct {
	Format   int32          `json:"format"`
	TileSize tileSize       `json:"tile_size"`
	TileSet  tileSet        `json:"tileset"`
	Layers   []tilemapLayer `json:"layers"`
}

// DecoratorNode 表示场景中的 Sprite2D 装饰节点。
type DecoratorNode struct {
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	Parent         string    `json:"parent"`
	Position       Vec2      `json:"position"`
	Scale          Vec2      `json:"scale,omitempty"`
	Ratation       float64   `json:"rotation,omitempty"`
	Pivot          Vec2      `json:"pivot,omitempty"`
	ZIndex         int32     `json:"z_index,omitempty"`
	ColliderType   string    `json:"collider_type,omitempty"`   // 碰撞体形状：none、auto、circle、rect、capsule 或 polygon。
	ColliderPivot  Vec2      `json:"collider_pivot,omitempty"`  // 碰撞体相对节点原点的偏移。
	ColliderParams []float64 `json:"collider_params,omitempty"` // 形状参数；多边形时为交替排列的顶点 x、y 坐标。
}

// spriteNode 表示场景中实例化的预制体节点。
type spriteNode struct {
	Name           string                 `json:"name"`
	Path           string                 `json:"path"`
	Parent         string                 `json:"parent"`
	Position       Vec2                   `json:"position"`
	Scale          Vec2                   `json:"scale,omitempty"`
	Ratation       float64                `json:"rotation,omitempty"`
	ZIndex         int32                  `json:"z_index,omitempty"`
	Pivot          Vec2                   `json:"pivot,omitempty"`
	ColliderType   string                 `json:"collider_type,omitempty"`   // 碰撞体形状：none、auto、circle、rect、capsule 或 polygon。
	ColliderPivot  Vec2                   `json:"collider_pivot,omitempty"`  // 碰撞体相对节点原点的偏移。
	ColliderParams []float64              `json:"collider_params,omitempty"` // 形状参数；多边形时为交替排列的顶点 x、y 坐标。
	Properties     map[string]interface{} `json:"properties,omitempty"`
}

// TscnMapData 表示 JSON 输出的根结构。
type TscnMapData struct {
	TileMap    tileMapData     `json:"tilemap"`
	Decorators []DecoratorNode `json:"decorators"`
	Sprites    []spriteNode    `json:"sprites"`
}

const tilemapRelDir = "tilemaps"

func (v Vec2) ToVec2() mathf.Vec2 {
	return mathf.NewVec2(v.X, v.Y)
}

func (v Vec2) Add(other Vec2) Vec2 {
	return Vec2{X: v.X + other.X, Y: v.Y + other.Y}
}

func (v Vec2) Sub(other Vec2) Vec2 {
	return Vec2{X: v.X - other.X, Y: v.Y - other.Y}
}

// Runtime utilities for parsing tile data
func ConvertData(data *TscnMapData) {
	for _, item := range data.Decorators {
		item.Path = toTilemapPath(item.Path)
	}
}

func LoadTilemaps(datas *TscnMapData, funcSetTile func(texturePath string, points []float64), funcSetLayer func(layerIndex int64),
	funcPlaceTiles func(positions []float64, texturePath string, layerIndex int64)) {
	paths := make(map[int32]string)
	for _, item := range datas.TileMap.TileSet.Sources {
		paths[item.ID] = toTilemapPath(item.TexturePath)
		points := make([]float64, 0)
		for _, tile := range item.Tiles {
			pts := tile.Physics.CollisionPoints
			for _, p := range pts {
				points = append(points, p.X, p.Y)
			}
		}
		funcSetTile(paths[item.ID], points)
	}
	for _, layer := range datas.TileMap.Layers {
		layerId := int64(layer.ZIndex)
		funcSetLayer(layerId)
		tileData := layer.TileData
		tileSizeX, tileSizeY := datas.TileMap.TileSize.Width, datas.TileMap.TileSize.Height
		tiles := parseTileData(tileData)
		slices.SortFunc(tiles, func(a, b tileInstance) int {
			return int(a.SourceID - b.SourceID)
		})
		lastId := int32(-1)
		path := ""
		positions := make([]float64, 0, len(tiles)*2)
		for _, tile := range tiles {
			if lastId != tile.SourceID {
				if len(positions) > 0 {
					funcPlaceTiles(positions, path, layerId)
				}
				positions = positions[:0]
				lastId = tile.SourceID
				path = paths[tile.SourceID]
			}
			x, y := tile.TileCoords.X*tileSizeX, tile.TileCoords.Y*tileSizeY
			positions = append(positions, float64(x), float64(y))
		}
		if len(positions) > 0 {
			funcPlaceTiles(positions, path, layerId)
		}
	}
}

func toTilemapPath(p string) string {
	if strings.HasPrefix(p, tilemapRelDir) {
		return p
	}
	return path.Join(tilemapRelDir, p)
}

// TileMapParser provides utilities for parsing compact tile data
// ParseTileData converts compact tile data array to tile instances
// New format: [source_id, tile_x, tile_y, atlas_x, atlas_y] (5 elements per tile)
// Where source_id is the ID of the tileset source,
// tile_x and tile_y are the tile coordinates in the map,
// and atlas_x and atlas_y are the coordinates in the tileset texture.
func parseTileData(tileData []int32) []tileInstance {
	tileCount := len(tileData) / 5
	tiles := make([]tileInstance, 0, tileCount)

	for i := 0; i < len(tileData); i += 5 {
		if i+4 >= len(tileData) {
			break
		}

		sourceID := tileData[i]
		tileX := tileData[i+1]
		tileY := tileData[i+2]
		atlasX := tileData[i+3]
		atlasY := tileData[i+4]

		tile := tileInstance{
			TileCoords:  vec2i{X: tileX, Y: tileY},
			SourceID:    sourceID,
			AtlasCoords: vec2i{X: atlasX, Y: atlasY},
		}

		tiles = append(tiles, tile)
	}

	return tiles
}
