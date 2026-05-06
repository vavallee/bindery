import { Recommendation } from '../api/client'
import RecommendationCard from './RecommendationCard'

interface RecommendationRowProps {
  title: string
  recommendations: Recommendation[]
  onDismiss: (id: number) => void
  onAdd: (id: number) => void
  onExcludeAuthor: (authorName: string) => void
}

export default function RecommendationRow({ title, recommendations, onDismiss, onAdd, onExcludeAuthor }: RecommendationRowProps) {
  if (recommendations.length === 0) return null

  return (
    <div className="mb-8">
      <h3 className="text-base font-semibold mb-3 text-slate-700 dark:text-zinc-300">{title}</h3>
      <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-4">
        {recommendations.map(rec => (
          <RecommendationCard
            key={rec.id}
            rec={rec}
            onDismiss={onDismiss}
            onAdd={onAdd}
            onExcludeAuthor={onExcludeAuthor}
          />
        ))}
      </div>
    </div>
  )
}
