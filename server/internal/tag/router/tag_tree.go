package router

import (
	"mayfly-go/internal/tag/api"
	"mayfly-go/internal/tag/imsg"
	"mayfly-go/pkg/biz"
	"mayfly-go/pkg/ioc"
	"mayfly-go/pkg/req"

	"github.com/gin-gonic/gin"
)

func InitTagTreeRouter(router *gin.RouterGroup) {
	m := new(api.TagTree)
	biz.ErrIsNil(ioc.Inject(m))

	tagTree := router.Group("/tag-trees")
	{
		reqs := [...]*req.Conf{
			// 获取标签树列表
			req.NewGet("", m.GetTagTree),

			// 根据条件获取标签
			req.NewGet("query", m.ListByQuery),

			req.NewPost("", m.SaveTagTree).Log(req.NewLogSaveI(imsg.LogTagSave)).RequiredPermissionCode("tag:save"),

			req.NewDelete(":id", m.DelTagTree).Log(req.NewLogSaveI(imsg.LogTagDelete)).RequiredPermissionCode("tag:del"),

			req.NewPost("/moving", m.MovingTag).Log(req.NewLogSaveI(imsg.LogTagMove)).RequiredPermissionCode("tag:save"),

			req.NewGet("/resources/:rtype/tag-paths", m.TagResources),

			req.NewGet("/resources/count", m.CountTagResource),

			// 获取关联的标签id列表
			req.NewGet("/relate/:relateType/:relateId", m.GetRelateTagIds),
		}

		req.BatchSetGroup(tagTree, reqs[:])
	}
}
